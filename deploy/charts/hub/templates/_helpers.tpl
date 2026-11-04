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
{{- else if or .Values.auth.bootstrapAdmin.password (dig "externalSecret" "enabled" false .Values.auth.bootstrapAdmin) -}}{{ include "hub.fullname" . }}-bootstrap-admin
{{- end -}}
{{- end -}}

{{/* Secret holding the datasource secrets encryption key (D-025). */}}
{{- define "hub.datasourceKeySecret" -}}{{ default (printf "%s-datasource-key" (include "hub.fullname" .)) .Values.onboarding.secretsKey.existingSecret }}{{- end -}}

{{/* "true" when the hub gets a datasource secrets key (D-025). */}}
{{- define "hub.datasourceKeyEnabled" -}}
{{- $k := .Values.onboarding.secretsKey -}}
{{- if or $k.create $k.existingSecret (dig "externalSecret" "enabled" false $k) -}}true{{- end -}}
{{- end -}}

{{- define "hub.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}{{ .Values.serviceAccount.name | default (include "hub.fullname" .) }}
{{- else -}}{{ .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/* Database CA bundle source ("" = none) and the sslrootcert path in the URL. */}}
{{- define "hub.dbCAEnabled" -}}
{{- $ca := .Values.database.caBundle | default dict -}}
{{- if or $ca.existingConfigMap $ca.existingSecret $ca.pem -}}true{{- end -}}
{{- end -}}
{{- define "hub.dbSSLRootCert" -}}
{{- .Values.database.sslrootcert | default (ternary "/etc/hub/db-ca/ca.pem" "" (eq (include "hub.dbCAEnabled" .) "true")) -}}
{{- end -}}
