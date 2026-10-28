package kv

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisOptions struct {
	URL      string // redis:// or rediss://; a username may be in the URL, a password may not
	Password string // from GHDASH_REDIS_PASSWORD
	CAFile   string // optional PEM bundle for rediss://
	Prefix   string // prepended to every key; "yana:" by default in config
}

// Redis is a Store over Redis or Valkey. Every key is prefixed so several
// installations can share one server.
type Redis struct {
	c      *redis.Client
	prefix string
}

var _ Store = (*Redis)(nil)

// deleteIfEqual runs as one script so nothing can take the key between the
// compare and the delete; a GET then DEL from here could release a lock that
// another replica has just acquired.
var deleteIfEqual = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`)

// NewRedis connects and pings once with ctx, so a misconfigured server fails
// at startup rather than on the first login. No error it returns contains the
// URL: it can carry a username, and a mistyped one a password.
func NewRedis(ctx context.Context, opts RedisOptions) (*Redis, error) {
	u, err := url.Parse(opts.URL)
	if err != nil {
		return nil, errors.New("kv: redis url does not parse")
	}
	// A password in the URL ends up in shell history, process listings and
	// any log line that prints the config, so it is refused outright.
	if _, ok := u.User.Password(); ok {
		return nil, errors.New("kv: put the redis password in GHDASH_REDIS_PASSWORD, not the URL")
	}
	o, err := redis.ParseURL(opts.URL)
	if err != nil {
		return nil, errors.New("kv: redis url must be redis://host:port[/db] or rediss://…")
	}
	if opts.Password != "" {
		o.Password = opts.Password
	}
	if opts.CAFile != "" {
		if o.TLSConfig == nil {
			return nil, errors.New("kv: a redis CA file needs a rediss:// URL")
		}
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("kv: read redis CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("kv: redis CA file holds no PEM certificates")
		}
		o.TLSConfig.RootCAs = pool
	}
	if o.TLSConfig != nil {
		o.TLSConfig.MinVersion = tls.VersionTLS12
	}
	o.DialTimeout = 5 * time.Second
	o.ReadTimeout = 2 * time.Second
	o.WriteTimeout = 2 * time.Second

	r := &Redis{c: redis.NewClient(o), prefix: opts.Prefix}
	if err := r.c.Ping(ctx).Err(); err != nil {
		r.c.Close()
		return nil, fmt.Errorf("kv: ping redis at %s: %w", o.Addr, err)
	}
	return r, nil
}

func (r *Redis) k(key string) string { return r.prefix + key }

func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := r.c.Get(ctx, r.k(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("kv: get: %w", err)
	}
	return v, nil
}

// Set passes ttl straight through: 0 is "no expiry" in go-redis, which is
// what the interface promises. KeepTTL would leave an old expiry in place.
func (r *Redis) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := r.c.Set(ctx, r.k(key), val, ttl).Err(); err != nil {
		return fmt.Errorf("kv: set: %w", err)
	}
	return nil
}

func (r *Redis) SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error) {
	ok, err := r.c.SetNX(ctx, r.k(key), val, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("kv: setnx: %w", err)
	}
	return ok, nil
}

func (r *Redis) DeleteIfEqual(ctx context.Context, key string, val []byte) (bool, error) {
	n, err := deleteIfEqual.Run(ctx, r.c, []string{r.k(key)}, val).Int64()
	if err != nil {
		return false, fmt.Errorf("kv: delete if equal: %w", err)
	}
	return n == 1, nil
}

func (r *Redis) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	full := make([]string, len(keys))
	for i, key := range keys {
		full[i] = r.k(key)
	}
	if err := r.c.Del(ctx, full...).Err(); err != nil {
		return fmt.Errorf("kv: delete: %w", err)
	}
	return nil
}

func (r *Redis) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if ttl <= 0 {
		n, err := r.c.Del(ctx, r.k(key)).Result()
		if err != nil {
			return fmt.Errorf("kv: expire: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	}
	ok, err := r.c.PExpire(ctx, r.k(key), ttl).Result()
	if err != nil {
		return fmt.Errorf("kv: expire: %w", err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

func (r *Redis) ZAdd(ctx context.Context, key string, score float64, member string) error {
	if err := r.c.ZAdd(ctx, r.k(key), redis.Z{Score: score, Member: member}).Err(); err != nil {
		return fmt.Errorf("kv: zadd: %w", err)
	}
	return nil
}

func (r *Redis) ZRangeByScore(ctx context.Context, key string, max float64, limit int64) ([]string, error) {
	args := redis.ZRangeArgs{
		Key:     r.k(key),
		ByScore: true,
		Start:   "-inf",
		Stop:    strconv.FormatFloat(max, 'f', -1, 64),
	}
	if limit > 0 {
		args.Count = limit
	}
	out, err := r.c.ZRangeArgs(ctx, args).Result()
	if err != nil {
		return nil, fmt.Errorf("kv: zrange: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (r *Redis) ZRem(ctx context.Context, key string, members ...string) error {
	if len(members) == 0 {
		return nil
	}
	m := make([]any, len(members))
	for i, member := range members {
		m[i] = member
	}
	if err := r.c.ZRem(ctx, r.k(key), m...).Err(); err != nil {
		return fmt.Errorf("kv: zrem: %w", err)
	}
	return nil
}

func (r *Redis) Ping(ctx context.Context) error {
	if err := r.c.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("kv: ping: %w", err)
	}
	return nil
}

func (r *Redis) Close() error { return r.c.Close() }
