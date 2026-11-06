{{- define "webhook.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Cluster-scoped objects carry the namespace so two releases cannot collide. */}}
{{- define "webhook.clusterName" -}}
{{- printf "%s-%s" .Release.Namespace (include "webhook.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "webhook.labels" -}}
app.kubernetes.io/name: kerf-webhook
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: kerf
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "webhook.selectorLabels" -}}
app.kubernetes.io/name: kerf-webhook
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "webhook.certificate" -}}{{ include "webhook.fullname" . }}-serving{{- end -}}
{{- define "webhook.tlsSecret" -}}{{ include "webhook.fullname" . }}-tls{{- end -}}

{{- define "webhook.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}{{ .Values.serviceAccount.name | default (include "webhook.fullname" .) }}
{{- else -}}{{ required "serviceAccount.name is required with serviceAccount.create: false" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{- /*
The self-signed serving certificate of tls.selfSigned (development). Generated once per render and shared by the
Secret and the MutatingWebhookConfiguration (whichever template runs first fills .Values._selfSigned); an existing
Secret of the release is reused, so an upgrade keeps the certificate. Output: JSON {ca, crt, key}, base64.
*/ -}}
{{- define "webhook.selfSigned" -}}
{{- if not (hasKey .Values "_selfSigned") -}}
{{- $existing := lookup "v1" "Secret" .Release.Namespace (include "webhook.tlsSecret" .) -}}
{{- if and $existing $existing.data (index $existing.data "tls.crt") (index $existing.data "tls.key") (index $existing.data "ca.crt") -}}
{{- $_ := set .Values "_selfSigned" (dict "ca" (index $existing.data "ca.crt") "crt" (index $existing.data "tls.crt") "key" (index $existing.data "tls.key")) -}}
{{- else -}}
{{- $days := int .Values.tls.selfSigned.validityDays -}}
{{- $cn := printf "%s.%s.svc" (include "webhook.fullname" .) .Release.Namespace -}}
{{- $ca := genCA (printf "%s-ca" (include "webhook.fullname" .)) $days -}}
{{- $cert := genSignedCert $cn nil (list $cn (printf "%s.cluster.local" $cn)) $days $ca -}}
{{- $_ := set .Values "_selfSigned" (dict "ca" (b64enc $ca.Cert) "crt" (b64enc $cert.Cert) "key" (b64enc $cert.Key)) -}}
{{- end -}}
{{- end -}}
{{- toJson (get .Values "_selfSigned") -}}
{{- end -}}
