{{/* Collects every problem first, so a user fixes them in one round. */}}
{{- define "yana.validateAuth" -}}
{{- $a := .Values.auth -}}
{{- if not (has $a.mode (list "server" "local")) -}}
{{- fail (printf "auth.mode must be server or local, not %q" $a.mode) -}}
{{- end -}}
{{- if eq $a.mode "server" -}}
{{- $missing := list -}}
{{- if not $a.publicURL -}}
{{- $missing = append $missing "auth.publicURL (the URL users open, e.g. https://yana.example.com)" -}}
{{- else if not (regexMatch "^https?://[^/?#]+/?$" $a.publicURL) -}}
{{- $missing = append $missing (printf "auth.publicURL must be scheme and host only, no path: %q" $a.publicURL) -}}
{{- end -}}
{{- if not $a.clientId -}}
{{- $missing = append $missing "auth.clientId (the OAuth App or GitHub App client ID)" -}}
{{- end -}}
{{- if .Values.valkey.enabled -}}
{{- if $a.redis.url -}}
{{- $missing = append $missing "set either valkey.enabled=true (the bundled Valkey) or auth.redis.url with valkey.enabled=false, not both" -}}
{{- end -}}
{{- if or $a.redis.caConfigMap $a.redis.caSecret -}}
{{- $missing = append $missing "auth.redis.caConfigMap/caSecret are for an external rediss:// Redis; the bundled Valkey does not use TLS inside the namespace" -}}
{{- end -}}
{{- else if not $a.redis.url -}}
{{- $missing = append $missing "auth.redis.url (redis:// or rediss:// of the external Redis/Valkey, needed with valkey.enabled=false)" -}}
{{- else if not (regexMatch "^rediss?://" $a.redis.url) -}}
{{- $missing = append $missing (printf "auth.redis.url must start with redis:// or rediss://: %q" $a.redis.url) -}}
{{- else if regexMatch "^rediss?://[^/@]*:[^/@]*@" $a.redis.url -}}
{{- $missing = append $missing "auth.redis.url must not contain a password; put it in the Secret's redis-password key" -}}
{{- end -}}
{{- if and $a.redis.caConfigMap $a.redis.caSecret -}}
{{- $missing = append $missing "set at most one of auth.redis.caConfigMap and auth.redis.caSecret" -}}
{{- end -}}
{{- if not (or $a.allowedOrgs $a.allowedUsers $a.allowAnyUser) -}}
{{- $missing = append $missing "an access policy: auth.allowedOrgs, auth.allowedUsers, or auth.allowAnyUser=true" -}}
{{- end -}}
{{- if $missing -}}
{{- $keys := printf "%s and %s (and %s if your Redis requires one)" $a.secretKeys.clientSecret $a.secretKeys.sessionKeys $a.secretKeys.redisPassword -}}
{{- if .Values.valkey.enabled -}}
{{- $keys = printf "%s, %s and %s (the bundled Valkey's password)" $a.secretKeys.clientSecret $a.secretKeys.sessionKeys $a.secretKeys.redisPassword -}}
{{- end -}}
{{- fail (printf "server mode (the default) needs:\n  - %s\nThe Secret %s must also hold %s. See docs/usage/github-app.md, or set auth.mode=local for a single-user install." (join "\n  - " $missing) (include "yana.secretName" .) $keys) -}}
{{- end -}}
{{- else if ne (int .Values.replicaCount) 1 -}}
{{- fail "local mode keeps its state on one ReadWriteOnce volume and must run exactly one replica; leave replicaCount at 1 or use auth.mode=server" -}}
{{- end -}}
{{- end -}}
