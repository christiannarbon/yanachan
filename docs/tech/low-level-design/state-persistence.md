# State persistence

`backend/internal/state/state.go`

Two JSON files in one directory. That is the entire persistence layer.

| File | Holds | Corrupt file behaviour |
| --- | --- | --- |
| `settings.json` | teams, orgs, limit, window override, view flags | startup error |
| `session.json` | auth mode, login, token, created stamp | ignored, user signs in again |

The asymmetry is deliberate. A corrupt session costs a sign-in; a corrupt
settings file means somebody's followed teams are about to be silently replaced
with defaults, and failing loudly is the kinder outcome.

## The store

```go
type Store struct {
    mu           sync.RWMutex
    settingsPath string
    sessionPath  string
    settings     Settings
    session      *Session
}
```

Everything goes through the mutex, and `Session()` returns a **copy** — a caller
holding a pointer into the store could otherwise mutate the token in place. That
is what `TestSessionReturnsCopy` is for.

## File modes

The directory is created `0700` and then `Chmod`ed to `0700` explicitly, because
`MkdirAll` only applies the mode to directories it actually creates: a state
directory that already existed — the pre-rename one, or a path handed in through
`GHDASH_STATE_DIR` — keeps whatever it had, which may well be world-readable.
The chmod is defence in depth and its failure is not worth aborting over.

Both files are written `0600`. `TestPersistedFilesAreOwnerOnly` asserts it.

## Atomic writes

Write to a temp file in the same directory, then `rename` over the target. A
crash mid-write leaves either the old file or the new one, never half of either.
`TestSaveLeavesNoTempFile` checks the temp file does not survive a successful
write.

## Normalization

`normalize` runs on every load and every save, so an out-of-range value is
**corrected rather than rejected**:

| Field | Rule |
| --- | --- |
| `Limit` | outside 1–100 → the configured default |
| `WindowHours` | outside 0–720 (`MaxWindowHours`) → 0, the business-day rule |
| `Teams`, `Orgs` | trimmed, case-insensitively deduped, truncated to `MaxRefs` (200) |

Shape validation is separate and lives in the API handler, where it can return a
400 with a message worth reading: a team must contain a slash, an org must not.

`MaxWindowHours` is 30 days. Past roughly 292 years the hours-to-`Duration`
multiplication overflows outright, and nothing here is useful at that range.

## Where the directory is

`config.defaultStateDir()`:

1. `<UserConfigDir>/yana-chan-4k` if it exists.
2. Otherwise `<UserConfigDir>/github-dashboarder` if *that* exists — the
   pre-rename directory.
3. Otherwise `<UserConfigDir>/yana-chan-4k`, which will be created.

The fallback matters because the expensive thing to lose is the session: losing
it means authenticating again. Once the new directory exists it always wins, so
this is a one-way migration that never runs twice.

`GHDASH_STATE_DIR` overrides all of it, and is what the container and the pod
set (`/data`).

## Sign-out

`ClearSession` removes `session.json` and drops the in-memory copy.
`settings.json` is untouched — the teams you follow are not a secret and are not
worth making you re-enter.

## Server mode: sessions

`backend/internal/session/session.go`

In server mode there is no `session.json`. Each session is one key in
Redis/Valkey, reached through `kv.Store`, and nothing is written to disk.

| Key | Value | TTL |
| --- | --- | --- |
| `session:<hex sha256(session id)>` | JSON record, tokens sealed | effective expiry + `RevokeGrace` (1h) |
| `session-expiry` (sorted set) | member = the hex hash, score = effective expiry in unix seconds | none |

Both sit under `GHDASH_REDIS_PREFIX`.

**The cookie carries the ID; Redis never does.** The key is named by the
SHA-256 of the ID, so someone reading Redis — a dump, a replica, `MONITOR` —
cannot turn a key back into a cookie. The ID is 32 random bytes,
base64url-encoded to 43 characters, and anything of another shape is rejected
before it is hashed, so a garbage cookie costs no round-trip.

**The tokens are sealed.** The access and refresh tokens are sealed with the
[`GHDASH_SESSION_KEYS`](configuration-reference.md) keyring, AES-256-GCM, using
the hex hash as the additional data, so a sealed token copied into another session's value does not open. A
value that does not open — its key was dropped from the ring, or it was
tampered with — is deleted and treated as not found: the user signs in again.

**Effective expiry** is `min(LastSeenAt + idle, CreatedAt + max)`. Past it, a
session is not found, whatever the TTL says. The key is not deleted at that
point: the reaper still needs the token to revoke it at GitHub. Updating a
session always keeps the stored `CreatedAt`, so nothing can carry it past the
maximum age.

**The TTL** on every write is the effective expiry plus an hour of grace. It is
the backstop that removes the key even if the reaper never runs. A write whose
TTL would already be zero deletes the session instead.

**The `session-expiry` index** is updated on every write and cleared on every
delete. The reaper reads it to find sessions past their effective expiry,
oldest first, revokes their tokens and deletes them by hash.

`LastSeenAt` is written back at most once a minute per session, so a board
refreshing every few seconds is not a Redis write every few seconds.

## Server mode: settings

`backend/internal/state/users.go`

In server mode each person follows their own teams and orgs. There is no
`settings.json`: each user's settings are one key in Redis/Valkey, under
`GHDASH_REDIS_PREFIX`.

| Key | Value | TTL |
| --- | --- | --- |
| `user:<github user id>:settings` | JSON `state.Settings` | none |

**The numeric user ID, not the login.** A login can be renamed and later
registered by someone else, who would then inherit the old owner's settings.
The ID never changes hands.

**No TTL and not sealed.** Settings hold no secret — a list of teams and orgs
and a few view flags — so there is nothing to seal, and nothing to expire.
Reading a user with no key returns the defaults and writes nothing.

The same [normalization](#normalization) as local mode runs on every read and
every save.

**A corrupt value is treated as no settings.** It is logged with the user ID,
the defaults are returned, and the next save replaces it. One user's broken
value must not lock them out.

**A Redis error is surfaced, not hidden.** If the store cannot be reached, the
read fails and the API answers with a 5xx. Quietly showing the defaults
instead would invite the user to save them over their real settings.

There is no in-memory cache: with several replicas, one would serve stale
settings after another replica saved.
