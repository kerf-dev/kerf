{{- define "webhook.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Cluster-scoped objects carry the namespace so two releases cannot collide. */}}
{{- define "webhook.clusterName" -}}
{{- printf "%s-%s" .Release.Namespace (include "webhook.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "webhook.labels" -}}
app.kubernetes.io/name: resource-optimizer-webhook
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: resource-optimizer
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "webhook.selectorLabels" -}}
app.kubernetes.io/name: resource-optimizer-webhook
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "webhook.certificate" -}}{{ include "webhook.fullname" . }}-serving{{- end -}}
{{- define "webhook.tlsSecret" -}}{{ include "webhook.fullname" . }}-tls{{- end -}}

{{- define "webhook.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}{{ .Values.serviceAccount.name | default (include "webhook.fullname" .) }}
{{- else -}}{{ required "serviceAccount.name is required with serviceAccount.create: false" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}
