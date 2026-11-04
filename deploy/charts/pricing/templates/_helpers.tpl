{{- define "pricing.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "pricing.labels" -}}
app.kubernetes.io/name: pricing
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: resource-optimizer
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "pricing.selectorLabels" -}}
app.kubernetes.io/name: pricing
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "pricing.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "pricing.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "pricing.caEnabled" -}}
{{- $ca := .Values.hub.caBundle | default dict -}}
{{- if or $ca.existingConfigMap $ca.existingSecret $ca.pem -}}true{{- end -}}
{{- end -}}
