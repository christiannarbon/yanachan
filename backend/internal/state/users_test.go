package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
)

func newUserSettings(store kv.Store) *UserSettings {
	return NewUserSettings(store, DefaultSettings(25))
}

func mustGetUser(t *testing.T, u *UserSettings, userID int64) Settings {
	t.Helper()
	got, err := u.Get(context.Background(), userID)
	if err != nil {
		t.Fatalf("Get(%d): %v", userID, err)
	}
	return got
}

func mustSaveUser(t *testing.T, u *UserSettings, userID int64, in Settings) Settings {
	t.Helper()
	out, err := u.Save(context.Background(), userID, in)
	if err != nil {
		t.Fatalf("Save(%d): %v", userID, err)
	}
	return out
}

func TestUserSettingsDefaultsWhenMissing(t *testing.T) {
	mem := kv.NewMemory(nil)
	got := mustGetUser(t, newUserSettings(mem), 42)
	if !reflect.DeepEqual(got, DefaultSettings(25)) {
		t.Fatalf("got %+v, want defaults", got)
	}
	if _, err := mem.Get(context.Background(), userKey(42)); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("reading wrote a key: %v", err)
	}
}

func TestUserSettingsRoundTrip(t *testing.T) {
	u := newUserSettings(kv.NewMemory(nil))
	in := DefaultSettings(25)
	in.Teams = []string{"acme/core"}
	in.Orgs = []string{"acme"}
	mustSaveUser(t, u, 42, in)
	if got := mustGetUser(t, u, 42); !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v, want %+v", got, in)
	}
}

func TestUserSettingsIsolated(t *testing.T) {
	u := newUserSettings(kv.NewMemory(nil))
	a := DefaultSettings(25)
	a.Teams = []string{"acme/core"}
	b := DefaultSettings(25)
	b.Orgs = []string{"globex"}
	b.Limit = 50
	mustSaveUser(t, u, 42, a)
	mustSaveUser(t, u, 43, b)

	if got := mustGetUser(t, u, 42); !reflect.DeepEqual(got, a) {
		t.Errorf("user 42 got %+v, want %+v", got, a)
	}
	if got := mustGetUser(t, u, 43); !reflect.DeepEqual(got, b) {
		t.Errorf("user 43 got %+v, want %+v", got, b)
	}
	if got := mustGetUser(t, u, 44); !reflect.DeepEqual(got, DefaultSettings(25)) {
		t.Errorf("user 44 got %+v, want defaults", got)
	}
}

func TestUserSettingsNormalizes(t *testing.T) {
	u := newUserSettings(kv.NewMemory(nil))
	orgs := make([]string, MaxRefs+5)
	for i := range orgs {
		orgs[i] = fmt.Sprintf("org%d", i)
	}
	in := Settings{
		Teams: []string{"acme/core", "ACME/Core", " acme/web "},
		Orgs:  orgs,
		Limit: 0,
	}
	saved := mustSaveUser(t, u, 42, in)
	got := mustGetUser(t, u, 42)
	if !reflect.DeepEqual(saved, got) {
		t.Fatalf("Save returned %+v, Get returned %+v", saved, got)
	}
	if got.Limit != 25 {
		t.Errorf("Limit = %d, want 25", got.Limit)
	}
	if want := []string{"acme/core", "acme/web"}; !reflect.DeepEqual(got.Teams, want) {
		t.Errorf("Teams = %q, want %q", got.Teams, want)
	}
	if len(got.Orgs) != MaxRefs {
		t.Errorf("len(Orgs) = %d, want %d", len(got.Orgs), MaxRefs)
	}
}

func TestUserSettingsNoTTL(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	u := newUserSettings(kv.NewMemory(func() time.Time { return now }))
	in := DefaultSettings(25)
	in.Teams = []string{"acme/core"}
	mustSaveUser(t, u, 42, in)
	now = now.AddDate(1, 0, 0)
	if got := mustGetUser(t, u, 42); !reflect.DeepEqual(got, in) {
		t.Fatalf("a year later got %+v, want %+v", got, in)
	}
}

func TestUserSettingsCorruptValue(t *testing.T) {
	mem := kv.NewMemory(nil)
	u := newUserSettings(mem)
	if err := mem.Set(context.Background(), "user:42:settings", []byte("{not json"), 0); err != nil {
		t.Fatal(err)
	}
	if got := mustGetUser(t, u, 42); !reflect.DeepEqual(got, DefaultSettings(25)) {
		t.Fatalf("got %+v, want defaults", got)
	}
	in := DefaultSettings(25)
	in.Orgs = []string{"acme"}
	mustSaveUser(t, u, 42, in)
	if got := mustGetUser(t, u, 42); !reflect.DeepEqual(got, in) {
		t.Fatalf("after save got %+v, want %+v", got, in)
	}
}

type downStore struct {
	kv.Store
	err error
}

func (d downStore) Get(context.Context, string) ([]byte, error) { return nil, d.err }

func TestUserSettingsStoreErrorPropagates(t *testing.T) {
	down := errors.New("down")
	got, err := newUserSettings(downStore{err: down}).Get(context.Background(), 42)
	if !errors.Is(err, down) {
		t.Fatalf("err = %v, want it to wrap the store error", err)
	}
	if !reflect.DeepEqual(got, Settings{}) {
		t.Fatalf("got %+v alongside an error, want the zero value", got)
	}
}

func TestUserSettingsRejectsBadID(t *testing.T) {
	u := newUserSettings(kv.NewMemory(nil))
	ctx := context.Background()
	for _, id := range []int64{0, -1} {
		if _, err := u.Save(ctx, id, DefaultSettings(25)); err == nil {
			t.Errorf("Save(%d) succeeded", id)
		}
	}
	if _, err := u.Get(ctx, 0); err == nil {
		t.Error("Get(0) succeeded")
	}
}

func TestUserSettingsReturnsCopies(t *testing.T) {
	u := newUserSettings(kv.NewMemory(nil))
	in := DefaultSettings(25)
	in.Teams = []string{"acme/core"}
	mustSaveUser(t, u, 42, in)

	got := mustGetUser(t, u, 42)
	got.Teams[0] = "changed/team"
	if again := mustGetUser(t, u, 42); again.Teams[0] != "acme/core" {
		t.Fatalf("Teams[0] = %q after mutating an earlier copy", again.Teams[0])
	}

	defaults := mustGetUser(t, u, 99)
	defaults.Teams = append(defaults.Teams, "leaked/team")
	if again := mustGetUser(t, u, 99); len(again.Teams) != 0 {
		t.Fatalf("defaults now hold %q", again.Teams)
	}
}
