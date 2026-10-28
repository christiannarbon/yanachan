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

const (
	// touchEvery bounds how often LastSeenAt is written back; otherwise every
	// board refresh would be a write.
	touchEvery = time.Minute
	// RevokeGrace keeps a dead session's key around long enough for the
	// reaper to revoke its token at GitHub. The TTL is only the backstop.
	RevokeGrace = time.Hour
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

// Store needs no mutex of its own: kv.Store is safe for concurrent use, two
// replicas touching one session write equivalent values, and token rotation
// is serialised by the caller's refresh lock.
type Store struct {
	kv        kv.Store
	ring      *seal.Keyring
	idle, max time.Duration
	now       func() time.Time
}

func New(store kv.Store, ring *seal.Keyring, idle, max time.Duration) *Store {
	return &Store{kv: store, ring: ring, idle: idle, max: max, now: time.Now}
}

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

func (s *Store) Touch(ctx context.Context, id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	h := hashID(id)
	rec, err := s.live(ctx, h)
	if err != nil {
		return err
	}
	now := s.now()
	if now.Sub(rec.LastSeenAt) < touchEvery {
		return nil
	}
	rec.LastSeenAt = now
	return s.save(ctx, h, rec)
}

// Update keeps the stored CreatedAt, so no caller can carry a session past
// its maximum age.
func (s *Store) Update(ctx context.Context, id string, rec Record) error {
	if !validID(id) {
		return ErrNotFound
	}
	h := hashID(id)
	old, err := s.live(ctx, h)
	if err != nil {
		return err
	}
	rec.CreatedAt = old.CreatedAt
	return s.save(ctx, h, rec)
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
// session's token.
func (s *Store) LoadForRevocation(ctx context.Context, hash string) (Record, error) {
	if !validHash(hash) {
		return Record{}, ErrNotFound
	}
	return s.load(ctx, hash)
}

// DeleteHash is the reaper's delete. It takes a hash because that is all the
// reaper ever has.
func (s *Store) DeleteHash(ctx context.Context, hash string) error {
	if !validHash(hash) {
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

// load treats a value it cannot read as gone, and removes it: either its key
// was rotated out of the ring or it was tampered with, and either way the
// user signs in again.
func (s *Store) load(ctx context.Context, hash string) (Record, error) {
	raw, err := s.kv.Get(ctx, key(hash))
	if errors.Is(err, kv.ErrNotFound) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("session: load: %w", err)
	}
	rec, ok := s.decode(raw, hash)
	if !ok {
		if err := s.remove(ctx, hash); err != nil {
			return Record{}, err
		}
		return Record{}, ErrNotFound
	}
	return rec, nil
}

func (s *Store) decode(raw []byte, hash string) (Record, bool) {
	var st stored
	if err := json.Unmarshal(raw, &st); err != nil {
		return Record{}, false
	}
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

// save seals the tokens with the hash as aad, so a sealed token copied into
// another session's value does not open.
func (s *Store) save(ctx context.Context, hash string, rec Record) error {
	exp := s.effectiveExpiry(rec)
	ttl := exp.Sub(s.now()) + RevokeGrace
	if ttl <= 0 {
		return s.remove(ctx, hash)
	}
	token, err := s.ring.Seal([]byte(rec.Token), hash)
	if err != nil {
		return fmt.Errorf("session: seal: %w", err)
	}
	var refresh string
	if rec.RefreshToken != "" {
		if refresh, err = s.ring.Seal([]byte(rec.RefreshToken), hash); err != nil {
			return fmt.Errorf("session: seal: %w", err)
		}
	}
	raw, err := json.Marshal(stored{
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
		return fmt.Errorf("session: encode: %w", err)
	}
	if err := s.kv.Set(ctx, key(hash), raw, ttl); err != nil {
		return fmt.Errorf("session: save: %w", err)
	}
	if err := s.kv.ZAdd(ctx, expiryIndex, float64(exp.Unix()), hash); err != nil {
		return fmt.Errorf("session: index: %w", err)
	}
	return nil
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
