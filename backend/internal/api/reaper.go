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
	// reapMargin is how much of the lock must be left to reap another session.
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
	ended, err := a.reapSessions(ctx, deadline)
	if err != nil {
		return ended, err
	}
	if _, err := a.retryRevocations(ctx, deadline); err != nil {
		return ended, err
	}
	return ended, nil
}

// reapSessions checks the deadline before every session, not every batch: a
// batch of slow revocations could otherwise outlive the lock and let a second
// replica reap the same sessions.
func (a *serverAuth) reapSessions(ctx context.Context, deadline time.Time) (int, error) {
	ended := 0
	for {
		hashes, err := a.sessions.Expired(ctx, a.now(), reapBatch)
		if err != nil {
			return ended, err
		}
		skipped := 0
		for _, h := range hashes {
			if err := ctx.Err(); err != nil {
				return ended, err
			}
			if !a.now().Before(deadline) {
				return ended, nil
			}
			done, err := a.reap(ctx, h)
			if errors.Is(err, session.ErrUnreadable) {
				skipped++
				continue
			}
			if err != nil {
				return ended, err
			}
			if done {
				ended++
			}
		}
		// Only unreadable sessions left: the next query would return them
		// again, so stop until a replica with their key comes along.
		if len(hashes) < reapBatch || skipped == len(hashes) {
			return ended, nil
		}
	}
}

// reap revokes and deletes one expired session. A failed revocation does not
// keep the session alive; the token goes to the retry queue instead.
func (a *serverAuth) reap(ctx context.Context, hash string) (bool, error) {
	rec, err := a.sessions.LoadForRevocation(ctx, hash)
	if errors.Is(err, session.ErrUnreadable) {
		return false, err
	}
	if errors.Is(err, session.ErrNotFound) {
		return false, a.sessions.DeleteHash(ctx, hash)
	}
	if err != nil {
		return false, err
	}
	a.revoke(rec.Token, rec.Login)
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
