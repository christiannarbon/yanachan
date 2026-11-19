{{- define "yana.validate" -}}
{{- $exposed := or .Values.ingress.enabled .Values.httpRoute.enabled (ne .Values.service.type "ClusterIP") -}}
{{- if and $exposed (not (include "yana.serverMode" .)) (not .Values.exposure.authenticatedBy) -}}
{{- fail "yanachan in local mode has no login: anything that reaches it is signed in as the stored GitHub token. In local mode, ingress, httpRoute and non-ClusterIP services need exposure.authenticatedBy naming the authenticating proxy in front. Or use auth.mode=server, where every user signs in with GitHub (see docs/tech/architecture/security.md)." -}}
{{- end -}}
{{- if and .Values.ingress.enabled (not .Values.ingress.hosts) -}}
{{- fail "ingress.enabled needs at least one entry in ingress.hosts" -}}
{{- end -}}
{{- if and .Values.httpRoute.enabled (not .Values.httpRoute.parentRefs) -}}
{{- fail "httpRoute.enabled needs at least one entry in httpRoute.parentRefs" -}}
{{- end -}}
{{- range splitList "," (include "yana.allowedHosts" .) -}}
{{- if hasPrefix "*" . -}}
{{- fail (printf "%q is a wildcard host. The app matches Host headers exactly, so it would refuse every request: list each name users open in allowedHosts and in the ingress or route hosts" .) -}}
{{- end -}}
{{- end -}}
{{- if and $exposed (not (include "yana.serverMode" .)) (not (include "yana.allowedHosts" .)) -}}
{{- fail "local mode is exposed but answers to no host name, so it would refuse every request with 403: set allowedHosts to the name users open through the authenticating proxy" -}}
{{- end -}}
{{- if and (include "yana.valkey.enabled" .) (not (has .Values.valkey.persistence.whenDeleted (list "Retain" "Delete"))) -}}
{{- fail (printf "valkey.persistence.whenDeleted must be Retain or Delete, not %q" .Values.valkey.persistence.whenDeleted) -}}
{{- end -}}
{{- end -}}

{{- define "yana.authenticatedBy" -}}
{{- if include "yana.serverMode" . }}github-sign-in{{ else }}{{ .Values.exposure.authenticatedBy }}{{ end -}}
{{- end -}}

{{- define "yana.allowedHosts" -}}
{{- $hosts := list -}}
{{- range .Values.allowedHosts }}{{ $hosts = append $hosts . }}{{ end -}}
{{- if and (include "yana.serverMode" .) .Values.auth.publicURL }}{{ $hosts = append $hosts (urlParse .Values.auth.publicURL).host }}{{ end -}}
{{- if .Values.ingress.enabled }}{{ range .Values.ingress.hosts }}{{ $hosts = append $hosts .host }}{{ end }}{{ end -}}
{{- if .Values.httpRoute.enabled }}{{ range .Values.httpRoute.hostnames }}{{ $hosts = append $hosts . }}{{ end }}{{ end -}}
{{- $hosts | uniq | join "," -}}
{{- end -}}
