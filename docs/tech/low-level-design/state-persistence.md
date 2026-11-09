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

In server mode there is no `session.json` and no `settings.json`. Each session
is one key in Redis/Valkey, reached through `kv.Store`, and nothing is written
to disk: `GHDASH_STATE_DIR` is ignored, and a local-mode state directory on the
same machine is neither read nor changed. `servermode_e2e_test.go` asserts that
a sign-in, a settings round-trip, a rotation and a sign-out leave the working
directory and the state directory empty.

| Key | Value | TTL |
| --- | --- | --- |
| `session:<hex sha256(session id)>` | JSON record, tokens sealed | effective expiry + `RevokeGrace` (24h) |
| `session-expiry` (sorted set) | member = the hex hash, score = effective expiry in unix seconds | none |

Both sit under `GHDASH_REDIS_PREFIX`.

**The cookie carries the ID; Redis never does.** The key is named by the
SHA-256 of the ID, so someone reading Redis — a dump, a replica, `MONITOR` —
cannot turn a key back into a cookie. The ID is 32 random bytes,
base64url-encoded to 43 characters, and anything of another shape is rejected
before it is hashed, so a garbage cookie costs no round-trip.

**The tokens are sealed.** The access and refresh tokens are sealed with the
[`GHDASH_SESSION_KEYS`](configuration-reference.md) keyring, AES-256-GCM, using
the hex hash as the additional data, so a sealed token copied into another
session's value does not open.

A value that does not open is treated as not found but **left in place**. Its
key may be on another replica — during a rolling key rotation the new pods seal
with a key the old pods do not have yet — and that replica can still use the
session and, later, revoke its token. The TTL removes it in the end. A value
that is not even JSON is deleted: nothing can ever read it.

**Effective expiry** is `min(LastSeenAt + idle, CreatedAt + max)`. Past it, a
session is not found, whatever the TTL says. The key is not deleted at that
point: the reaper still needs the token to revoke it at GitHub. Changing a
session always keeps the stored `CreatedAt`, so nothing can carry it past the
maximum age.

**Every change is a compare-and-set.** After `Create`, nothing rewrites a
session blindly. `Modify` reads the stored value, applies a change to the
fields that writer owns — `LastSeenAt` for a touch, `VerifiedAt` for the access
re-check, the token fields for a rotation — and writes back only if the value is
still the one it read (`SetIfEqual`, a script in Redis). If another replica
wrote in between, it re-reads and applies the change again, up to five times.
So a touch can never put back a token that a rotation on another replica has
just replaced.

**The TTL** on every write is the effective expiry plus a day of grace. It is
the backstop that removes the key even if the reaper never runs, and long
enough that a reaper outage of a few hours still finds every token to revoke. A write whose
TTL would already be zero deletes the session instead.

**The `session-expiry` index** is updated on every write and cleared on every
delete. The reaper reads it to find sessions past their effective expiry,
oldest first, revokes their tokens and deletes them by hash.

`LastSeenAt` is written back at most once a minute per session, so a board
refreshing every few seconds is not a Redis write every few seconds.

## Server mode: rotation and sign-out

`backend/internal/api/serverauth.go`

No GitHub token is used past its maximum age, `MaxTokenAge`:
`GHDASH_GITHUB_APP_TOKEN_MAX_AGE` for a session holding a refresh token,
`GHDASH_OAUTH_APP_TOKEN_MAX_AGE` for one without, each falling back to
`GHDASH_TOKEN_MAX_AGE`. Every request checks its session's token before using
it; a token is also renewed five minutes before GitHub's own expiry.

- **GitHub App** (a refresh token is stored): the server refreshes it. One
  replica at a time holds `lock:refresh:<hex sha256(session id)>` (30s TTL);
  the others wait up to two seconds for the new token, then use the current
  one if it is still valid. GitHub rotates the refresh token on every refresh.
- **OAuth App** (no refresh token): nothing can be refreshed. The first
  request after the maximum age **retires** the token: it is removed from the
  session and revoked at GitHub at once, so it does not stay valid while the
  user is away. API calls that need GitHub then answer `401 {"reauth": true}`;
  settings stay readable. The next sign-in on that browser puts a new token
  into the same session; the session ID does not change.

A refresh and its save run to completion even if the request that started them
is cancelled: GitHub spends the old refresh token the moment it answers, so a
refresh that is not saved would end the session. If a sign-in replaced the
token while the refresh ran, the sign-in's token wins and the refreshed one is
revoked.

| Key | Value | TTL |
| --- | --- | --- |
| `lock:refresh:<hex sha256(session id)>` | random owner string | 30s |

A session ends — deleted, and its token revoked at GitHub — when a refresh is
refused, the refresh token has expired, GitHub answers a request with 401 (the
app was revoked, or the token was dropped), another user signs in on the same
browser, access is withdrawn, or the user signs out. Sign-out also expires both
cookies.

When GitHub refuses a token with 401, the session ends only if that token is
still the session's own. A request that started before a re-authorisation and
fails with the old token leaves the renewed session alone and answers `503`
("try again").

**Revocation never blocks, and is never forgotten.** If GitHub cannot be
reached, the token is sealed into a retry queue and the reaper tries again with
backoff: 5 minutes, doubling to at most 6 hours, 8 attempts in all.

| Key | Value | TTL |
| --- | --- | --- |
| `revoke:<random id>` | JSON: the token sealed with the key name as aad, the login, the attempt count | 7 days |
| `revoke-queue` (sorted set) | member = the random id, score = when to try next | none |

## Server mode: the reaper

`backend/internal/api/reaper.go`

Every replica runs a reaper every five minutes. One at a time takes
`lock:reaper`; the others skip that round. The holder reads `session-expiry` for
sessions past their effective expiry, oldest first, 100 at a time, and for each
one revokes the token at GitHub and deletes the key and its index entry. A
failed revocation goes to the retry queue, and the session is deleted anyway.
The deadline is checked before every session, so a slow batch cannot outlive
the lock. Sessions this replica cannot open are skipped and left for one that
can. The same pass then works through the due entries of the retry queue.

| Key | Value | TTL |
| --- | --- | --- |
| `lock:reaper` | random owner string | 4m |

If no reaper runs for a whole day, the key's TTL removes the session and its
token is then only invalidated by GitHub or by the user.

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
