# Server mode behind a route with TLS

The recommended way to give a team the dashboard: every user signs in with
GitHub and sees what their own account can see. The chart exposes the pod
through an Ingress (`ingress-values.yaml`) or a Gateway API HTTPRoute
(`httproute-values.yaml`) and annotates it
`yana-chan-4k/authenticated-by: github-sign-in`.

Replace every `<placeholder>`, `your-org` and `yana.example.com` with your own values.
Commands run from the repository root.

## One rule

The route host, `auth.publicURL` and the OAuth App's callback URL must all name
the same host. The session cookie is scoped to the host users open, and GitHub
sends users back to `auth.publicURL` + `/api/auth/callback`. A route for
`yana.example.com` with `publicURL: https://dash.example.com` signs nobody in.
Use `https`: over plain `http` the session cookie goes out without `Secure`.

The chart adds the route hosts and the `auth.publicURL` host to
`GHDASH_ALLOWED_HOSTS` itself.

## Steps

1. Create the OAuth App as described in
   [GitHub OAuth App or GitHub App](../../../../docs/usage/github-app.md), with the
   callback URL:

   ```
   https://yana.example.com/api/auth/callback
   ```

   A GitHub App also works; its tokens only see repositories where it is
   installed (see that page). Put its client ID in `auth.clientId` and an
   organisation in `auth.allowedOrgs`.

2. Nothing to install for Redis. The chart runs its own Valkey and sets its
   password from the Secret below. To use a managed Redis or Valkey instead, set
   `valkey.enabled: false` and `auth.redis.url`.

3. Create the namespace and the Secret. The Valkey password is one you generate
   now:

   ```sh
   kubectl create namespace yana-chan-4k
   kubectl -n yana-chan-4k create secret generic yanachan \
     --from-literal=github-client-secret=<client-secret> \
     --from-literal=session-keys="$(head -c32 /dev/urandom | base64)" \
     --from-literal=redis-password="$(head -c24 /dev/urandom | base64)"
   ```

4. Install the chart, with the Ingress:

   ```sh
   helm install yanachan charts/yanachan -n yana-chan-4k \
     -f charts/yanachan/examples/server-mode/ingress-values.yaml
   ```

   or with the HTTPRoute (the cluster needs the Gateway API CRDs, and the
   Gateway's HTTPS listener holds the certificate):

   ```sh
   helm install yanachan charts/yanachan -n yana-chan-4k \
     -f charts/yanachan/examples/server-mode/httproute-values.yaml
   ```

5. Point DNS for `yana.example.com` at the ingress or gateway address:

   ```sh
   kubectl -n yana-chan-4k get ingress yanachan
   kubectl -n <gateway-namespace> get gateway <your-gateway>
   ```

6. Open `https://yana.example.com` and sign in.

## Notes

- Two replicas work because the app pod keeps no state of its own; sessions
  and settings live in Valkey. The bundled Valkey stays a single instance.
- `networkPolicy.enabled: true` admits pods in the release namespace to the
  dashboard port. Your ingress controller or gateway usually runs elsewhere:
  add it under `networkPolicy.ingressFrom`, for example
  `- namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: <controller-namespace>}}`.
  Egress is limited to DNS, HTTPS and Valkey.
