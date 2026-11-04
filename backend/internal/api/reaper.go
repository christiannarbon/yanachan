package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/session"
)

const (
	reapEvery   = 5 * time.Minute
	reapLockTTL = 4 * time.Minute
	reapBatch   = 100
	reaperLock  = "lock:reaper"
	// reapMargin is how much of the lock must be left to start another batch.
	reapMargin    = 30 * time.Second
	revokeTimeout = 10 * time.Second
)

// RunReaper revokes and deletes expired sessions every reapEvery until ctx
// ends. Every replica runs it; the lock:reaper key lets one of them work at
// a time. A no-op in local mode.
func (s *Server) RunReaper(ctx context.Context) {
	if s.server == nil {
		return
	}
	tick := time.NewTicker(reapEvery)
	defer tick.Stop()
	for {
		if _, err := s.server.reapOnce(ctx); err != nil && ctx.Err() == nil {
			log.Printf("session reaper: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// reapOnce is one locked pass. It reports how many sessions it ended.
func (a *serverAuth) reapOnce(ctx context.Context) (int, error) {
	owner, err := lockOwner()
	if err != nil {
		return 0, err
	}
	won, err := a.kv.SetNX(ctx, reaperLock, []byte(owner), reapLockTTL)
	if err != nil {
		return 0, fmt.Errorf("take lock: %w", err)
	}
	if !won {
		return 0, nil
	}
	defer a.kv.DeleteIfEqual(context.WithoutCancel(ctx), reaperLock, []byte(owner))

	deadline := a.now().Add(reapLockTTL - reapMargin)
	ended := 0
	for {
		hashes, err := a.sessions.Expired(ctx, a.now(), reapBatch)
		if err != nil {
			return ended, err
		}
		for _, h := range hashes {
			if err := ctx.Err(); err != nil {
				return ended, err
			}
			done, err := a.reap(ctx, h)
			if err != nil {
				return ended, err
			}
			if done {
				ended++
			}
		}
		if len(hashes) < reapBatch || !a.now().Before(deadline) {
			return ended, nil
		}
	}
}

// reap revokes and deletes one expired session. A failed revocation does not
// keep the session alive.
func (a *serverAuth) reap(ctx context.Context, hash string) (bool, error) {
	rec, err := a.sessions.LoadForRevocation(ctx, hash)
	if errors.Is(err, session.ErrNotFound) {
		return false, a.sessions.DeleteHash(ctx, hash)
	}
	if err != nil {
		return false, err
	}
	rctx, cancel := context.WithTimeout(ctx, revokeTimeout)
	err = a.web.Revoke(rctx, a.cfg.RESTEndpoint, rec.Token)
	cancel()
	if err != nil {
		log.Printf("token revocation failed for %s (%d): %v", rec.Login, rec.UserID, err)
	}
	if err := a.sessions.DeleteHash(ctx, hash); err != nil {
		return false, err
	}
	log.Printf("session expired: %s (%d)", rec.Login, rec.UserID)
	return true, nil
}

func lockOwner() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("lock owner: %w", err)
	}
	return hex.EncodeToString(b), nil
}
