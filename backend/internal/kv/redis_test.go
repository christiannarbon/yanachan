package kv_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv/kvtest"
)

// The Redis tests need a real server. make valkey-up starts one and
// make test-redis points these tests at it; without it they skip.
func redisOptions(t *testing.T) kv.RedisOptions {
	url := os.Getenv("GHDASH_TEST_REDIS_URL")
	if url == "" {
		t.Skip("GHDASH_TEST_REDIS_URL not set; run make valkey-up && make test-redis")
	}
	return kv.RedisOptions{URL: url, Password: os.Getenv("GHDASH_TEST_REDIS_PASSWORD"), Prefix: "yana-test:"}
}

func newRedis(t *testing.T, opts kv.RedisOptions) *kv.Redis {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := kv.NewRedis(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestRedisConformance(t *testing.T) {
	opts := redisOptions(t)
	kvtest.Run(t, func(t *testing.T) kv.Store { return newRedis(t, opts) }, nil)
}

func TestRedisPrefix(t *testing.T) {
	opts := redisOptions(t)
	ctx := context.Background()

	opts.Prefix = "p1:"
	p1 := newRedis(t, opts)
	opts.Prefix = "p2:"
	p2 := newRedis(t, opts)

	if err := p1.Set(ctx, "k", []byte("one"), time.Minute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p1.Delete(context.Background(), "k") })

	if _, err := p2.Get(ctx, "k"); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("p2 Get err = %v, want ErrNotFound", err)
	}

	o, err := redis.ParseURL(opts.URL)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Password != "" {
		o.Password = opts.Password
	}
	raw := redis.NewClient(o)
	defer raw.Close()
	if got, err := raw.Get(ctx, "p1:k").Result(); err != nil || got != "one" {
		t.Fatalf("raw GET p1:k = %q, %v; want one", got, err)
	}
}

func TestRedisRefusesPasswordInURL(t *testing.T) {
	_, err := kv.NewRedis(context.Background(), kv.RedisOptions{URL: "redis://u:secret@127.0.0.1:1/0"})
	if err == nil {
		t.Fatal("NewRedis accepted a password in the URL")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("the error repeats the password from the URL")
	}
}

func TestRedisCAFileNeedsTLS(t *testing.T) {
	_, err := kv.NewRedis(context.Background(), kv.RedisOptions{URL: "redis://127.0.0.1:1/0", CAFile: "ca.pem"})
	if err == nil {
		t.Fatal("NewRedis accepted a CA file with a redis:// URL")
	}
}

func TestRedisUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := kv.NewRedis(ctx, kv.RedisOptions{URL: "redis://127.0.0.1:1/0", Password: "hunter2"})
	if err == nil {
		t.Fatal("NewRedis succeeded against a closed port")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatal("the error contains the password")
	}
}
