# The bundled Valkey

In server mode the Helm chart runs its own Valkey next to the app, so an install
needs no Redis of its own. It is on by default (`valkey.enabled: true`) and never
renders in local mode, whatever `valkey.enabled` says. For what server mode keeps
in Redis and Valkey in general, see [Redis or Valkey](redis.md).

## What it is

- One StatefulSet with **one** Valkey pod, named `<release>-valkey`
  (`yanachan-valkey` with the release named `yanachan`), and a ClusterIP Service
  of the same name on port 6379. The app is pointed at it automatically;
  `auth.redis.url` stays empty.
- The image is the official `valkey/valkey`, pinned by tag and digest in
  `values.yaml`. `global.imageRegistry` replaces its registry, as it does for the
  app image.
- It runs with the same hardening as the app: non-root (the image's `valkey`
  user, 999:1000), read-only root filesystem, no capabilities, `RuntimeDefault`
  seccomp, no service account token. It passes Pod Security `restricted`.
- It is **not highly available**. While it restarts, the app's pods turn NotReady
  (`/api/ready`) and leave the Service until it is back. For replication or a
  managed service, use an external Redis (below).

## What its disk holds

Sessions, with each user's GitHub tokens **sealed** with the `session-keys` in
the Secret, and each user's settings, which are not secret. Nothing on that disk
opens without the session keys. The key layout is in
[Redis or Valkey](redis.md#what-is-stored).

## Who can reach it

The chart always renders a NetworkPolicy for it, whether or not the app's own
policy is on. It admits only the app's pods, on 6379, and allows no egress at
all. The probes run inside the container, so the kubelet needs no rule.

A NetworkPolicy does something only on a cluster whose network plugin enforces
it. Most managed clusters do, or can (on GKE, Dataplane V2 or network policy
enforcement). On one that does not, any pod in the cluster can open a connection
to the Service, and the password is the only thing in the way.

## The password

AUTH is always on. The password is the `redis-password` key of the release
Secret, the same one the app reads, so the two cannot disagree. With the bundled
Valkey that key is **required**: without it both pods wait in
`CreateContainerConfigError`.

```sh
kubectl -n <namespace> create secret generic yanachan \
  --from-literal=github-client-secret=<client-secret> \
  --from-literal=session-keys="$(head -c32 /dev/urandom | base64)" \
  --from-literal=redis-password="$(head -c24 /dev/urandom | base64)"
```

The password must not contain a double quote, a backslash or a newline; Valkey
refuses to start and says so if it does. A base64 value like the one above is
fine.

The password never appears in the container's arguments, the pod spec or the
ConfigMap. At start the container copies the config into a memory-backed
`emptyDir`, appends `requirepass` from an environment variable into that file
(mode 0600), and starts `valkey-server` with it. The probes read it from
`VALKEYCLI_AUTH`, not from `-a`.

`requirepass` sets the password of Valkey's `default` user, which can run every
command. That is acceptable here because nothing but the app can connect. An
external Redis shared with other software deserves the narrower ACL user in
[Redis or Valkey](redis.md#hardening).

**Rotating it:** change the key in the Secret, then restart both, Valkey first:

```sh
kubectl -n <namespace> rollout restart statefulset/yanachan-valkey
kubectl -n <namespace> rollout status statefulset/yanachan-valkey
kubectl -n <namespace> rollout restart deploy/yanachan
```

Between the two restarts the app cannot authenticate and is NotReady. Sessions
survive when persistence is on.

## Persistence

On by default: append-only file with `appendfsync everysec` on a 1Gi PVC
(`data-yanachan-valkey-0`), so a Valkey restart keeps everyone signed in and
keeps their settings. At most about a second of writes can be lost in a crash.

`valkey.persistence.enabled: false` uses an `emptyDir` and turns the AOF off.
That costs this: **every Valkey restart, node drain or reschedule signs everyone
out and resets everyone's settings.** Use it only for a try-out.

`valkey.maxmemory` (default `192mb`, below the 256Mi memory limit) caps Valkey's
memory with eviction off. A full Valkey refuses writes, so sign-ins fail loudly,
rather than silently evicting someone's session.

## Uninstalling

The PVC is kept on `helm uninstall` (`valkey.persistence.whenDeleted: Retain`).
A reinstall with the same release name and the same `session-keys` picks the
sessions up again. To forget everything, delete it yourself:

```sh
kubectl -n <namespace> delete pvc data-yanachan-valkey-0
```

`whenDeleted: Delete` removes it with the release.

## Backups

You do not need to back it up: losing it signs everyone out and resets
settings, nothing more. If you do back it up, **never put the PVC's backup and
the Secret's `session-keys` in the same place.** Either alone yields no token;
together they yield every live user's GitHub token.

## Using an external Redis instead

For a managed service (Memorystore, ElastiCache, Azure Cache) or an existing
Redis or Valkey, turn the bundled one off and give the URL:

```yaml
valkey:
  enabled: false
auth:
  redis:
    url: rediss://<redis-host>:6378/0
```

The password, if any, stays in the Secret's `redis-password` key, which is then
optional. Setting both `valkey.enabled: true` and `auth.redis.url` fails the
render. Requirements and hardening for an external Redis are in
[Redis or Valkey](redis.md).
