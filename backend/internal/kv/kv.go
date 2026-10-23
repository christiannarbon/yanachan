// Package kv is the small key-value surface server mode keeps its state
// behind: sessions, per-user settings and cross-replica locks. Redis or
// Valkey in production, memory in tests.
package kv

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("kv: not found")

// Store takes logical keys; any prefix is the implementation's business.
type Store interface {
	// Get mirrors GET and returns ErrNotFound for a missing key.
	Get(ctx context.Context, key string) ([]byte, error)
	// Set mirrors SET … PX; ttl 0 means no expiry and clears any previous one.
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	// SetNX mirrors SET … NX PX and reports whether the key was set.
	SetNX(ctx context.Context, key string, val []byte, ttl time.Duration) (bool, error)
	// DeleteIfEqual is a compare-and-delete script, used to release a lock.
	DeleteIfEqual(ctx context.Context, key string, val []byte) (bool, error)
	// Delete mirrors DEL; a missing key is not an error.
	Delete(ctx context.Context, keys ...string) error
	// Expire mirrors PEXPIRE; ttl <= 0 deletes the key.
	Expire(ctx context.Context, key string, ttl time.Duration) error
	// ZAdd mirrors ZADD.
	ZAdd(ctx context.Context, key string, score float64, member string) error
	// ZRangeByScore mirrors ZRANGE … BYSCORE LIMIT: score <= max, ascending.
	ZRangeByScore(ctx context.Context, key string, max float64, limit int64) ([]string, error)
	// ZRem mirrors ZREM.
	ZRem(ctx context.Context, key string, members ...string) error
	// Ping mirrors PING.
	Ping(ctx context.Context) error
	Close() error
}
