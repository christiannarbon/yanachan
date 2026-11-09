# Redis or Valkey for server mode

Server mode keeps its sessions and each user's settings in Redis or Valkey, so
the app itself writes nothing to disk and can run more than one replica. Local
mode does not use Redis at all.

## What is stored

Every key sits under `GHDASH_REDIS_PREFIX` (default `yana:`).

| Key | Holds | Expires |
| --- | --- | --- |
| `session:<hash>` | One sign-in: login, GitHub user ID, timestamps, and the GitHub tokens **sealed** with `GHDASH_SESSION_KEYS` | With the session, plus a day so its token can be revoked |
| `session-expiry` | When each session expires, so expired ones can be found and revoked | Never; entries are removed with their session |
| `user:<github user id>:settings` | The teams, orgs and view options that user follows. No secret | Never |
| `lock:refresh:<hash>`, `lock:reaper` | Short locks so only one replica refreshes a token or revokes expired sessions at a time | 30 seconds, 4 minutes |
| `revoke:<id>`, `revoke-queue` | Tokens GitHub could not revoke yet, **sealed**, waiting for a retry | 7 days at most |

`<hash>` is the SHA-256 of the session cookie's value. The cookie itself is
never stored, and no token is stored in a form that opens without the session
keys. Details: [state persistence](../tech/low-level-design/state-persistence.md#server-mode-sessions).

## Requirements

- **Redis 6.2 or newer**, or **Valkey**. The app uses `ZRANGE … BYSCORE`, which
  Redis added in 6.2. Valkey 9.1 is what the tests run against.
- No modules. A single endpoint; Redis Cluster and Sentinel are not supported.
- Memory: small. A session is a few hundred bytes, a user's settings less.
- **Persistence is optional.** Without it, a Redis restart signs everyone out
  and resets everyone's settings. Turn it on if settings matter. A persisted
  file holds only sealed tokens and non-secret settings.

## Hardening

- [ ] **Require AUTH.** Either a password (`GHDASH_REDIS_PASSWORD`), or an ACL
      user limited to the app's prefix and commands. The username goes in the
      URL, the password never does:

      ```
      ACL SETUSER yana on >PASSWORD ~yana:* resetchannels -@all +get +set +del +pexpire +zadd +zrange +zrem +ping +eval +evalsha +hello +client|setinfo
      ```

      ```sh
      GHDASH_REDIS_URL=rediss://yana@<redis-host>:6378/0
      GHDASH_REDIS_PASSWORD=<password>
      ```

      Tested on Valkey 9.1. `client|setinfo` exists from Redis 7.2; on older
      Redis leave it out (the client treats it as optional). Add `+select` if
      the URL names a database other than `/0`.

      To keep the password off a command line, put the same `user` line in an
      ACL file and start the server with `--aclfile`.
- [ ] **Use TLS** where the server supports it: a `rediss://` URL. For a private
      CA, point `GHDASH_REDIS_CA_FILE` at its PEM bundle. TLS 1.2 is the minimum.
- [ ] **Restrict the network.** Only the app's pods should reach Redis. In
      Kubernetes, a NetworkPolicy admitting the app on the Redis port.
- [ ] **Do not expose Redis** outside the cluster or host.
- [ ] **Do not back up Redis and the Secret holding `GHDASH_SESSION_KEYS` to the
      same place.** Either alone yields no token; together they yield every live
      one. See the [threat model](../tech/architecture/security.md#threat-model-server-mode).
- [ ] **Sharing an instance?** Give every install its own `GHDASH_REDIS_PREFIX`,
      and its own ACL user limited to that prefix.

## Managed services

Any of these works; none is preferred.

- **Memorystore for Valkey** or **Memorystore for Redis** (Google Cloud). TLS:
  use `rediss://`, with the instance's CA in `GHDASH_REDIS_CA_FILE`.
- **ElastiCache** for Valkey or Redis OSS (AWS). TLS: use `rediss://`.
- **Azure Cache for Redis**. TLS: use `rediss://`.

## Startup and readiness

On startup the server pings Redis with a 10-second timeout and exits if it
cannot reach it, so a wrong URL or password shows up at once, not at the first
sign-in. The log names the host and port, never the password.

While running:

- `GET /api/ready` returns `503` when Redis does not answer within a second, so
  a load balancer or Kubernetes stops sending traffic to that replica.
- `GET /api/health` never touches Redis, so a Redis outage does not restart the
  app.

## Trying it locally

```sh
make valkey-up      # Valkey on 127.0.0.1:16379, password yana-dev-only, no persistence
make valkey-down
```

The password is a placeholder for a throwaway container on loopback, not a
credential. For Docker, see [server mode in Docker](docker.md#server-mode).
