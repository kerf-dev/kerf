{{- define "hub.name" -}}hub{{- end -}}
{{- define "hub.fullname" -}}{{ .Release.Name }}{{- end -}}
{{- define "hub.labels" -}}
app.kubernetes.io/name: hub
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: kerf
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
{{- else if or .Values.auth.bootstrapAdmin.password (dig "externalSecret" "enabled" false .Values.auth.bootstrapAdmin) (include "hub.bootstrapGenerated" .) -}}{{ include "hub.fullname" . }}-bootstrap-admin
{{- end -}}
{{- end -}}

{{- /* "true" when the chart generates the bootstrap admin's password (auth.bootstrapAdmin.generate and no
other source; not with devSeed, whose users include their own admin). The admin must change it at first sign-in. */ -}}
{{- define "hub.bootstrapGenerated" -}}
{{- $ba := .Values.auth.bootstrapAdmin -}}
{{- if and (dig "generate" true $ba) $ba.username (not $ba.password) (not $ba.existingSecret) (not (dig "externalSecret" "enabled" false $ba)) (not .Values.devSeed.enabled) -}}true{{- end -}}
{{- end -}}

{{- /* Secret with the tokens of this release's pricing service and (mode all) agent ("" = none): umbrella
chart, global.mode hub or all, localTokens.enabled. */ -}}
{{- define "hub.localTokensSecret" -}}
{{- $mode := dig "mode" "" (.Values.global | default dict) | toString -}}
{{- if and (has $mode (list "hub" "all")) (dig "enabled" true (.Values.localTokens | default dict)) -}}
{{- include "hub.c.localTokensSecret" (dict "ctx" .) -}}
{{- end -}}
{{- end -}}

{{- /* A random value kept across upgrades: key of Secret name when it exists (Helm lookup), else a new
one. Printed base64-encoded, for a Secret's data. dict: ctx, name, key, length. */ -}}
{{- define "hub.stableSecretValue" -}}
{{- $existing := lookup "v1" "Secret" .ctx.Release.Namespace .name -}}
{{- if and $existing $existing.data (index $existing.data .key) -}}
{{- index $existing.data .key -}}
{{- else -}}
{{- randAlphaNum (.length | default 40) | b64enc -}}
{{- end -}}
{{- end -}}

{{- /* Secret with the OIDC client secret ("" = none). */ -}}
{{- define "hub.oidcSecret" -}}
{{- $cs := .Values.auth.oidc.clientSecret | default dict -}}
{{- if $cs.existingSecret -}}{{ $cs.existingSecret }}
{{- else if or $cs.value (dig "externalSecret" "enabled" false $cs) -}}{{ include "hub.fullname" . }}-oidc
{{- end -}}
{{- end -}}

{{/* Secret holding the datasource secrets encryption key. */}}
{{- define "hub.datasourceKeySecret" -}}{{ default (printf "%s-datasource-key" (include "hub.fullname" .)) .Values.onboarding.secretsKey.existingSecret }}{{- end -}}

{{/* "true" when the hub gets a datasource secrets key. */}}
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
