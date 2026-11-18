# Local mode behind an authenticating proxy

Local mode has no login of its own: it serves the dashboard as one stored
GitHub token, to anyone who reaches it. This example puts
[oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) in front, so only
members of your GitHub organisation get through.

Prefer [server mode](../server-mode/README.md) unless your organisation already
standardises on an SSO proxy and wants one shared token. Every user here sees
that token's view of GitHub, and acts as it.

## Why this shape

The dashboard never gets a route of its own. Its Service stays `ClusterIP` and
`yana-values.yaml` enables no Ingress or HTTPRoute, so the chart's exposure gate
does not fire. The only way in is oauth2-proxy's own Ingress (or HTTPRoute),
which forwards signed-in users to the dashboard's Service with the original
`Host`, `dash.example.internal`. That name is in `allowedHosts`, so the app
answers to it.

If you ever expose the dashboard itself in local mode, the chart refuses until
`exposure.authenticatedBy` names the proxy in front, and records that claim on
the object as `yana-chan-4k/authenticated-by`.

## Steps

Replace every `<placeholder>` and `dash.example.internal` with your own
values. Commands run from the repository root.

1. Create a GitHub OAuth App for oauth2-proxy with the callback URL
   `https://dash.example.internal/oauth2/callback`.

2. Create the namespace and the dashboard's token Secret. The token is the
   view every user gets; a fine-grained token with read-only pull request and
   organisation permissions is enough:

   ```sh
   kubectl create namespace yana-chan-4k
   kubectl -n yana-chan-4k create secret generic yanachan \
     --from-literal=gh-token=<token>
   ```

3. Install the dashboard:

   ```sh
   helm install yanachan charts/yanachan -n yana-chan-4k \
     -f charts/yanachan/examples/oauth2-proxy/yana-values.yaml
   ```

4. Create oauth2-proxy's Secret:

   ```sh
   kubectl -n yana-chan-4k create secret generic oauth2-proxy \
     --from-literal=client-id=<oauth2-proxy-client-id> \
     --from-literal=client-secret=<oauth2-proxy-client-secret> \
     --from-literal=cookie-secret="$(openssl rand -base64 32 | head -c 32)"
   ```

5. Install oauth2-proxy with `oauth2-proxy-values.yaml`:

   ```sh
   helm repo add oauth2-proxy https://oauth2-proxy.github.io/manifests
   helm install oauth2-proxy oauth2-proxy/oauth2-proxy -n yana-chan-4k \
     -f charts/yanachan/examples/oauth2-proxy/oauth2-proxy-values.yaml
   ```

6. Point DNS for `dash.example.internal` at the ingress address, open
   `https://dash.example.internal` and sign in with GitHub.
