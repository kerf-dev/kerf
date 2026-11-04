{{- define "hub.name" -}}hub{{- end -}}
{{- define "hub.fullname" -}}{{ .Release.Name }}{{- end -}}
{{- define "hub.labels" -}}
app.kubernetes.io/name: hub
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: resource-optimizer
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}
{{- define "hub.selectorLabels" -}}
app.kubernetes.io/name: hub
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "hub.dbSecret" -}}{{ default (printf "%s-db" (include "hub.fullname" .)) .Values.database.existingSecret }}{{- end -}}
