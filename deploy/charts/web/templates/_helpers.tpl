{{- define "web.fullname" -}}{{ .Release.Name }}{{- end -}}
{{- define "web.labels" -}}
app.kubernetes.io/name: web
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: resource-optimizer
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
{{- define "web.selectorLabels" -}}
app.kubernetes.io/name: web
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "web.tokenSecret" -}}{{ default (printf "%s-hub-token" (include "web.fullname" .)) .Values.token.existingSecret }}{{- end -}}
{{- define "web.tokenKey" -}}{{ if .Values.token.existingSecret }}{{ .Values.token.existingSecretKey }}{{ else }}token{{ end }}{{- end -}}
