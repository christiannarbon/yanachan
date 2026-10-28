package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
)

// UserSettings keeps one Settings document per GitHub user, for server
// mode. Keys use the numeric user ID, never the login: a login can be
// renamed and later claimed by someone else.
//
// There is no cache on purpose: with several replicas, one would serve
// stale settings after another replica saved.
type UserSettings struct {
	kv       kv.Store
	defaults Settings
}

var errBadUserID = errors.New("user settings need a positive user id")

func NewUserSettings(store kv.Store, defaults Settings) *UserSettings {
	return &UserSettings{kv: store, defaults: defaults}
}

func userKey(userID int64) string {
	return "user:" + strconv.FormatInt(userID, 10) + ":settings"
}

// Get returns defaults for a user with nothing saved, or whose value no
// longer parses, so one broken value cannot lock anyone out. A store error is
// returned instead: showing defaults then would invite the user to save them
// over their real settings.
func (u *UserSettings) Get(ctx context.Context, userID int64) (Settings, error) {
	if userID <= 0 {
		return Settings{}, errBadUserID
	}
	b, err := u.kv.Get(ctx, userKey(userID))
	if errors.Is(err, kv.ErrNotFound) {
		return copySettings(u.defaults), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("load settings: %w", err)
	}
	var loaded Settings
	if err := json.Unmarshal(b, &loaded); err != nil {
		log.Printf("settings for user %d unreadable, using defaults: %v", userID, err)
		return copySettings(u.defaults), nil
	}
	return copySettings(normalize(loaded, u.defaults.Limit)), nil
}

// Save stores settings without a TTL and unsealed: they hold no secret.
func (u *UserSettings) Save(ctx context.Context, userID int64, in Settings) (Settings, error) {
	if userID <= 0 {
		return Settings{}, errBadUserID
	}
	out := normalize(in, u.defaults.Limit)
	b, err := json.Marshal(out)
	if err != nil {
		return Settings{}, fmt.Errorf("encode settings: %w", err)
	}
	if err := u.kv.Set(ctx, userKey(userID), b, 0); err != nil {
		return Settings{}, fmt.Errorf("save settings: %w", err)
	}
	return copySettings(out), nil
}

func copySettings(in Settings) Settings {
	out := in
	out.Teams = append([]string{}, in.Teams...)
	out.Orgs = append([]string{}, in.Orgs...)
	return out
}
