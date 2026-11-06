{{- define "web.fullname" -}}{{ .Release.Name }}{{- end -}}
{{- define "web.labels" -}}
app.kubernetes.io/name: web
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: kerf
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
{{- define "web.selectorLabels" -}}
app.kubernetes.io/name: web
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- /* Values removed when sign-in became required: fail with how to upgrade instead of ignoring them. */ -}}
{{- define "web.removedValues" -}}
{{- if hasKey (.Values.auth | default dict) "anonymousRead" -}}
{{- fail "auth.anonymousRead (web.auth.anonymousRead in the kerf chart) was removed: Kerf always requires sign-in and has no anonymous read-only access any more. Delete it from your values, together with token (web.token), the read-only hub token the UI no longer uses. Upgrade notes: docs/auth.md" -}}
{{- end -}}
{{- if hasKey .Values "token" -}}
{{- fail "token (web.token in the kerf chart) was removed: the UI no longer holds a hub token, because Kerf always requires sign-in. Delete it from your values, and revoke the old ui token in Admin -> Access. Upgrade notes: docs/auth.md" -}}
{{- end -}}
{{- end -}}
{{- define "web.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}{{ .Values.serviceAccount.name | default (include "web.fullname" .) }}
{{- else -}}{{ .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}
