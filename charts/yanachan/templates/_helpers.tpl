{{- define "yana.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "yana.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{- define "yana.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Selectors are immutable: nothing that changes per version belongs here. */}}
{{- define "yana.selectorLabels" -}}
app.kubernetes.io/name: {{ include "yana.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "yana.labels" -}}
helm.sh/chart: {{ include "yana.chart" . }}
{{ include "yana.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: yanachan
{{- end }}

{{- define "yana.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "yana.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "yana.secretName" -}}
{{- default (include "yana.fullname" .) .Values.auth.existingSecret }}
{{- end }}

{{- define "yana.serverMode" -}}
{{- if eq .Values.auth.mode "server" }}true{{ end }}
{{- end }}

{{- define "yana.claimName" -}}
{{- default (printf "%s-state" (include "yana.fullname" .)) .Values.persistence.existingClaim }}
{{- end }}

{{- define "yana.image" -}}
{{- $registry := default .Values.image.registry .Values.global.imageRegistry }}
{{- $repo := .Values.image.repository }}
{{- if $registry }}{{ $repo = printf "%s/%s" $registry $repo }}{{ end }}
{{- if .Values.image.digest }}
{{- printf "%s@%s" $repo .Values.image.digest }}
{{- else }}
{{- printf "%s:%s" $repo (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
{{- end }}

{{- define "yana.valkey.fullname" -}}
{{- printf "%s-valkey" (include "yana.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/* Must differ from the app's selector, or the app's Service would route to Valkey. */}}
{{- define "yana.valkey.selectorLabels" -}}
app.kubernetes.io/name: {{ printf "%s-valkey" (include "yana.name" .) | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "yana.valkey.labels" -}}
helm.sh/chart: {{ include "yana.chart" . }}
{{ include "yana.valkey.selectorLabels" . }}
app.kubernetes.io/component: valkey
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: yanachan
{{- end }}

{{/* The bundled Valkey renders only in server mode. */}}
{{- define "yana.valkey.enabled" -}}
{{- if and (include "yana.serverMode" .) .Values.valkey.enabled }}true{{ end }}
{{- end }}

{{- define "yana.valkey.image" -}}
{{- $v := .Values.valkey.image }}
{{- $registry := default $v.registry .Values.global.imageRegistry }}
{{- $repo := $v.repository }}
{{- if $registry }}{{ $repo = printf "%s/%s" $registry $repo }}{{ end }}
{{- if $v.digest }}{{ printf "%s@%s" $repo $v.digest }}{{ else }}{{ printf "%s:%s" $repo $v.tag }}{{ end }}
{{- end }}

{{- define "yana.redisURL" -}}
{{- if include "yana.valkey.enabled" . }}
{{- printf "redis://%s:6379/0" (include "yana.valkey.fullname" .) }}
{{- else }}
{{- .Values.auth.redis.url }}
{{- end }}
{{- end }}
