{{- define "agent.fullname" -}}
{{- if contains "kerf" .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{- else -}}kerf-{{ .Release.Name | trunc 44 | trimSuffix "-" }}{{- end -}}
{{- end -}}

{{- define "agent.labels" -}}
app.kubernetes.io/name: kerf-agent
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: kerf
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "agent.selectorLabels" -}}
app.kubernetes.io/name: kerf-agent
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "agent.hubURL" -}}
{{- if .Values.stubHub.enabled -}}
http://{{ include "agent.fullname" . }}-stub-hub.{{ .Release.Namespace }}.svc:8080
{{- else -}}
{{ .Values.hub.url }}
{{- end -}}
{{- end -}}

{{- define "agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}{{ .Values.serviceAccount.name | default (include "agent.fullname" .) }}
{{- else -}}{{ required "serviceAccount.name is required with serviceAccount.create: false" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{- define "agent.caEnabled" -}}
{{- $ca := .Values.hub.caBundle | default dict -}}
{{- if or $ca.existingConfigMap $ca.existingSecret $ca.pem -}}true{{- end -}}
{{- end -}}
