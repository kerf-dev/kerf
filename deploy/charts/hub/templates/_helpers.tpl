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
{{- /* Secret with the bootstrap admin's password ("" = none). */ -}}
{{- define "hub.bootstrapSecret" -}}
{{- if .Values.auth.bootstrapAdmin.existingSecret -}}{{ .Values.auth.bootstrapAdmin.existingSecret }}
{{- else if .Values.auth.bootstrapAdmin.password -}}{{ include "hub.fullname" . }}-bootstrap-admin
{{- end -}}
{{- end -}}

{{/* Secret holding the datasource secrets encryption key (D-025). */}}
{{- define "hub.datasourceKeySecret" -}}{{ default (printf "%s-datasource-key" (include "hub.fullname" .)) .Values.onboarding.secretsKey.existingSecret }}{{- end -}}
