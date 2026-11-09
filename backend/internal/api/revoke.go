package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
)

// A token GitHub could not revoke is not forgotten: an OAuth App token never
// expires on its own. It waits, sealed, in a queue that the reaper drains.
const (
	revokeQueue       = "revoke-queue"
	revokeKeyPrefix   = "revoke:"
	revokeMaxAttempts = 8
	revokeKeyTTL      = 7 * 24 * time.Hour
	revokeFirstRetry  = 5 * time.Minute
	revokeMaxBackoff  = 6 * time.Hour
)

type pendingRevoke struct {
	Token    string `json:"t"` // sealed with the entry's key as aad
	Login    string `json:"l"`
	Attempts int    `json:"a"`
}

// revoke revokes token at GitHub now, or queues it for a retry. It never
// fails the caller: a sign-in or sign-out must not hang on GitHub. Not tied
// to any request, so a browser navigating away cannot cancel it.
func (a *serverAuth) revoke(token, login string) {
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), revokeTimeout)
	defer cancel()
	if err := a.web.Revoke(ctx, a.cfg.RESTEndpoint, token); err != nil {
		log.Printf("revoke failed: %s (%v); queued for retry", login, err)
		a.enqueueRevoke(token, login, 1)
	}
}

func revokeBackoff(attempts int) time.Duration {
	d := revokeFirstRetry
	for i := 1; i < attempts && d < revokeMaxBackoff; i++ {
		d *= 2
	}
	return min(d, revokeMaxBackoff)
}

func (a *serverAuth) enqueueRevoke(token, login string, attempts int) {
	id, err := lockOwner()
	if err != nil {
		log.Printf("revoke for %s not queued: %v", login, err)
		return
	}
	key := revokeKeyPrefix + id
	sealed, err := a.ring.Seal([]byte(token), key)
	if err != nil {
		log.Printf("revoke for %s not queued: %v", login, err)
		return
	}
	raw, err := json.Marshal(pendingRevoke{Token: sealed, Login: login, Attempts: attempts})
	if err != nil {
		log.Printf("revoke for %s not queued: %v", login, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.kv.Set(ctx, key, raw, revokeKeyTTL); err != nil {
		log.Printf("revoke for %s not queued: %v", login, err)
		return
	}
	due := a.now().Add(revokeBackoff(attempts))
	if err := a.kv.ZAdd(ctx, revokeQueue, float64(due.Unix()), id); err != nil {
		log.Printf("revoke for %s not queued: %v", login, err)
	}
}

// retryRevocations is one pass over the due entries, run under the reaper's
// lock. It reports how many it settled, revoked or given up on.
func (a *serverAuth) retryRevocations(ctx context.Context, deadline time.Time) (int, error) {
	ids, err := a.kv.ZRangeByScore(ctx, revokeQueue, float64(a.now().Unix()), reapBatch)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, id := range ids {
		if !a.now().Before(deadline) {
			break
		}
		key := revokeKeyPrefix + id
		raw, err := a.kv.Get(ctx, key)
		if errors.Is(err, kv.ErrNotFound) {
			if err := a.kv.ZRem(ctx, revokeQueue, id); err != nil {
				return settled, err
			}
			continue
		}
		if err != nil {
			return settled, err
		}
		var p pendingRevoke
		token := []byte(nil)
		if err := json.Unmarshal(raw, &p); err == nil {
			token, err = a.ring.Open(p.Token, key)
			if err != nil {
				// Sealed with a key another replica holds; it will get it.
				continue
			}
		}
		if err := a.dropRevoke(ctx, id); err != nil {
			return settled, err
		}
		settled++
		if token == nil {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, revokeTimeout)
		err = a.web.Revoke(rctx, a.cfg.RESTEndpoint, string(token))
		cancel()
		switch {
		case err == nil:
			log.Printf("revoked on retry: %s", p.Login)
		case p.Attempts+1 >= revokeMaxAttempts:
			log.Printf("revoke for %s gave up after %d attempts: %v", p.Login, p.Attempts+1, err)
		default:
			a.enqueueRevoke(string(token), p.Login, p.Attempts+1)
		}
	}
	return settled, nil
}

func (a *serverAuth) dropRevoke(ctx context.Context, id string) error {
	if err := a.kv.Delete(ctx, revokeKeyPrefix+id); err != nil {
		return err
	}
	return a.kv.ZRem(ctx, revokeQueue, id)
}
