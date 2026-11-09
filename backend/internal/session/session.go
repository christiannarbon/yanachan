// Package session stores server-mode sessions in a kv.Store, one key per
// session, named by the hash of the session ID with the tokens sealed.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/seal"
)

var ErrNotFound = errors.New("session not found")

// ErrUnreadable is a session sealed with a key this keyring does not hold:
// another replica may have a newer key. It is ErrNotFound to callers that
// only need to know whether there is a usable session, but it is never
// deleted, so a replica that can open it can still use or revoke it.
var ErrUnreadable = fmt.Errorf("%w: sealed with a key this replica lacks", ErrNotFound)

// ErrSkip, returned by a Modify function, means there is nothing to write.
var ErrSkip = errors.New("session: nothing to write")

// ErrConflict means other writers kept changing the session while Modify
// tried to apply its change.
var ErrConflict = errors.New("session: changed concurrently, try again")

// modifyAttempts bounds how often Modify re-reads after losing a race.
const modifyAttempts = 5

const (
	// touchEvery bounds how often LastSeenAt is written back; otherwise every
	// board refresh would be a write.
	touchEvery = time.Minute
	// RevokeGrace keeps a dead session's key around long enough for the
	// reaper to revoke its token at GitHub. The TTL is only the backstop.
	RevokeGrace = 24 * time.Hour
	expiryIndex = "session-expiry"
	idLen       = 43
)

type Record struct {
	UserID           int64
	Login            string
	Token            string    // plaintext in memory, sealed in the store
	RefreshToken     string    // GitHub App only; sealed in the store; empty for OAuth Apps
	TokenIssuedAt    time.Time // drives the rotation policy
	TokenExpiresAt   time.Time // zero = GitHub set no expiry (OAuth App)
	RefreshExpiresAt time.Time

	CreatedAt, LastSeenAt, VerifiedAt time.Time
}

type stored struct {
	UserID           int64     `json:"userId"`
	Login            string    `json:"login"`
	Token            string    `json:"token"`
	RefreshToken     string    `json:"refreshToken,omitempty"`
	TokenIssuedAt    time.Time `json:"tokenIssuedAt"`
	TokenExpiresAt   time.Time `json:"tokenExpiresAt"`
	RefreshExpiresAt time.Time `json:"refreshExpiresAt"`
	CreatedAt        time.Time `json:"createdAt"`
	LastSeenAt       time.Time `json:"lastSeenAt"`
	VerifiedAt       time.Time `json:"verifiedAt"`
}

// Store needs no mutex of its own: every change is a compare-and-set on the
// stored value, so concurrent writers on any replica never overwrite each
// other's fields; the loser re-reads and re-applies its change.
type Store struct {
	kv        kv.Store
	ring      *seal.Keyring
	idle, max time.Duration
	now       func() time.Time
}

func New(store kv.Store, ring *seal.Keyring, idle, max time.Duration) *Store {
	return &Store{kv: store, ring: ring, idle: idle, max: max, now: time.Now}
}

// SetClock replaces time.Now, so tests can share one fake clock with the kv
// store and the callers.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func (s *Store) Create(ctx context.Context, rec Record) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	now := s.now()
	rec.CreatedAt, rec.LastSeenAt = now, now
	if rec.VerifiedAt.IsZero() {
		rec.VerifiedAt = now
	}
	if rec.TokenIssuedAt.IsZero() {
		rec.TokenIssuedAt = now
	}
	if err := s.save(ctx, hashID(id), rec); err != nil {
		return "", err
	}
	return id, nil
}

// Get does not delete a session past its effective expiry: the reaper still
// needs the token to revoke it.
func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	if !validID(id) {
		return Record{}, ErrNotFound
	}
	return s.live(ctx, hashID(id))
}

// Touch moves LastSeenAt forward, at most once per touchEvery.
func (s *Store) Touch(ctx context.Context, id string) error {
	now := s.now()
	_, err := s.Modify(ctx, id, func(r *Record) error {
		if now.Sub(r.LastSeenAt) < touchEvery {
			return ErrSkip
		}
		r.LastSeenAt = now
		return nil
	})
	return err
}

// Modify applies fn to the current record and stores the result only if the
// session was not written in between; otherwise it re-reads and runs fn
// again. fn should change only the fields it owns, so that a concurrent
// change to other fields, such as a rotated token, survives. CreatedAt is
// always kept, so no caller can carry a session past its maximum age.
func (s *Store) Modify(ctx context.Context, id string, fn func(*Record) error) (Record, error) {
	if !validID(id) {
		return Record{}, ErrNotFound
	}
	h := hashID(id)
	for range modifyAttempts {
		raw, cur, err := s.read(ctx, h)
		if err != nil {
			return Record{}, err
		}
		if !s.now().Before(s.effectiveExpiry(cur)) {
			return Record{}, ErrNotFound
		}
		next := cur
		if err := fn(&next); err != nil {
			if errors.Is(err, ErrSkip) {
				return cur, nil
			}
			return Record{}, err
		}
		next.CreatedAt = cur.CreatedAt
		ok, err := s.replace(ctx, h, raw, next)
		if err != nil {
			return Record{}, err
		}
		if ok {
			return next, nil
		}
	}
	return Record{}, ErrConflict
}

// DeleteIf deletes the session only if match approves it and nothing has
// written to it since it was read. It returns the record it judged.
func (s *Store) DeleteIf(ctx context.Context, id string, match func(Record) bool) (Record, bool, error) {
	if !validID(id) {
		return Record{}, false, ErrNotFound
	}
	h := hashID(id)
	raw, rec, err := s.read(ctx, h)
	if err != nil {
		return Record{}, false, err
	}
	if !match(rec) {
		return rec, false, nil
	}
	ok, err := s.kv.DeleteIfEqual(ctx, key(h), raw)
	if err != nil {
		return Record{}, false, fmt.Errorf("session: delete: %w", err)
	}
	if !ok {
		return rec, false, nil
	}
	if err := s.kv.ZRem(ctx, expiryIndex, h); err != nil {
		return rec, true, fmt.Errorf("session: index: %w", err)
	}
	return rec, true, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return nil
	}
	return s.remove(ctx, hashID(id))
}

// Expired returns the hashes whose effective expiry is <= now, oldest first.
func (s *Store) Expired(ctx context.Context, now time.Time, limit int64) ([]string, error) {
	hashes, err := s.kv.ZRangeByScore(ctx, expiryIndex, float64(now.Unix()), limit)
	if err != nil {
		return nil, fmt.Errorf("session: expired: %w", err)
	}
	return hashes, nil
}

// KeyHash is the hex SHA-256 of a session ID: the suffix of its key, the aad
// its tokens are sealed with, and the suffix of its refresh lock's key.
func KeyHash(id string) string { return hashID(id) }

// LoadForRevocation ignores expiry; it is how the reaper reads a dead
// session's token. ErrUnreadable means another replica must do it.
func (s *Store) LoadForRevocation(ctx context.Context, hash string) (Record, error) {
	if !validHash(hash) {
		return Record{}, ErrNotFound
	}
	return s.load(ctx, hash)
}

// DeleteHash is the reaper's delete. It takes a hash because that is all the
// reaper ever has. An index entry that is not a hash is still dropped, or
// the reaper would find it again on every pass.
func (s *Store) DeleteHash(ctx context.Context, hash string) error {
	if !validHash(hash) {
		if err := s.kv.ZRem(ctx, expiryIndex, hash); err != nil {
			return fmt.Errorf("session: index: %w", err)
		}
		return nil
	}
	return s.remove(ctx, hash)
}

func newID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session: new id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validID runs before hashing so a garbage cookie costs no round-trip.
func validID(id string) bool {
	if len(id) != idLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func hashID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

func validHash(h string) bool {
	if len(h) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func key(hash string) string { return "session:" + hash }

func (s *Store) effectiveExpiry(r Record) time.Time {
	idle := r.LastSeenAt.Add(s.idle)
	if max := r.CreatedAt.Add(s.max); max.Before(idle) {
		return max
	}
	return idle
}

func (s *Store) live(ctx context.Context, hash string) (Record, error) {
	rec, err := s.load(ctx, hash)
	if err != nil {
		return Record{}, err
	}
	if !s.now().Before(s.effectiveExpiry(rec)) {
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (s *Store) load(ctx context.Context, hash string) (Record, error) {
	_, rec, err := s.read(ctx, hash)
	return rec, err
}

// read returns the stored bytes with the record, for compare-and-set. A value
// that is not JSON is removed: nothing can ever read it. A value that does
// not open is left alone: its key may be on another replica, which can still
// use it and revoke its token.
func (s *Store) read(ctx context.Context, hash string) ([]byte, Record, error) {
	raw, err := s.kv.Get(ctx, key(hash))
	if errors.Is(err, kv.ErrNotFound) {
		return nil, Record{}, ErrNotFound
	}
	if err != nil {
		return nil, Record{}, fmt.Errorf("session: load: %w", err)
	}
	var st stored
	if err := json.Unmarshal(raw, &st); err != nil {
		if err := s.remove(ctx, hash); err != nil {
			return nil, Record{}, err
		}
		return nil, Record{}, ErrNotFound
	}
	rec, ok := s.decode(st, hash)
	if !ok {
		return nil, Record{}, ErrUnreadable
	}
	return raw, rec, nil
}

func (s *Store) decode(st stored, hash string) (Record, bool) {
	token, err := s.ring.Open(st.Token, hash)
	if err != nil {
		return Record{}, false
	}
	var refresh []byte
	if st.RefreshToken != "" {
		if refresh, err = s.ring.Open(st.RefreshToken, hash); err != nil {
			return Record{}, false
		}
	}
	return Record{
		UserID:           st.UserID,
		Login:            st.Login,
		Token:            string(token),
		RefreshToken:     string(refresh),
		TokenIssuedAt:    st.TokenIssuedAt,
		TokenExpiresAt:   st.TokenExpiresAt,
		RefreshExpiresAt: st.RefreshExpiresAt,
		CreatedAt:        st.CreatedAt,
		LastSeenAt:       st.LastSeenAt,
		VerifiedAt:       st.VerifiedAt,
	}, true
}

// save writes rec unconditionally; it is for new sessions only. Every later
// write goes through Modify.
func (s *Store) save(ctx context.Context, hash string, rec Record) error {
	raw, exp, ttl, err := s.encode(hash, rec)
	if err != nil {
		return err
	}
	if ttl <= 0 {
		return s.remove(ctx, hash)
	}
	if err := s.kv.Set(ctx, key(hash), raw, ttl); err != nil {
		return fmt.Errorf("session: save: %w", err)
	}
	return s.index(ctx, hash, exp)
}

// replace writes rec only if the stored value is still old.
func (s *Store) replace(ctx context.Context, hash string, old []byte, rec Record) (bool, error) {
	raw, exp, ttl, err := s.encode(hash, rec)
	if err != nil {
		return false, err
	}
	if ttl <= 0 {
		return true, s.remove(ctx, hash)
	}
	ok, err := s.kv.SetIfEqual(ctx, key(hash), old, raw, ttl)
	if err != nil {
		return false, fmt.Errorf("session: save: %w", err)
	}
	if !ok {
		return false, nil
	}
	return true, s.index(ctx, hash, exp)
}

func (s *Store) index(ctx context.Context, hash string, exp time.Time) error {
	if err := s.kv.ZAdd(ctx, expiryIndex, float64(exp.Unix()), hash); err != nil {
		return fmt.Errorf("session: index: %w", err)
	}
	return nil
}

// encode seals the tokens with the hash as aad, so a sealed token copied into
// another session's value does not open.
func (s *Store) encode(hash string, rec Record) (raw []byte, exp time.Time, ttl time.Duration, err error) {
	exp = s.effectiveExpiry(rec)
	ttl = exp.Sub(s.now()) + RevokeGrace
	if ttl <= 0 {
		return nil, exp, ttl, nil
	}
	token, err := s.ring.Seal([]byte(rec.Token), hash)
	if err != nil {
		return nil, exp, ttl, fmt.Errorf("session: seal: %w", err)
	}
	var refresh string
	if rec.RefreshToken != "" {
		if refresh, err = s.ring.Seal([]byte(rec.RefreshToken), hash); err != nil {
			return nil, exp, ttl, fmt.Errorf("session: seal: %w", err)
		}
	}
	raw, err = json.Marshal(stored{
		UserID:           rec.UserID,
		Login:            rec.Login,
		Token:            token,
		RefreshToken:     refresh,
		TokenIssuedAt:    rec.TokenIssuedAt,
		TokenExpiresAt:   rec.TokenExpiresAt,
		RefreshExpiresAt: rec.RefreshExpiresAt,
		CreatedAt:        rec.CreatedAt,
		LastSeenAt:       rec.LastSeenAt,
		VerifiedAt:       rec.VerifiedAt,
	})
	if err != nil {
		return nil, exp, ttl, fmt.Errorf("session: encode: %w", err)
	}
	return raw, exp, ttl, nil
}

func (s *Store) remove(ctx context.Context, hash string) error {
	if err := s.kv.Delete(ctx, key(hash)); err != nil {
		return fmt.Errorf("session: delete: %w", err)
	}
	if err := s.kv.ZRem(ctx, expiryIndex, hash); err != nil {
		return fmt.Errorf("session: index: %w", err)
	}
	return nil
}
