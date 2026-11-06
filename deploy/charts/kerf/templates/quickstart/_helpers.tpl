{{- /*
The laptop quickstart (docs/quickstart.md): everything under quickstart.* is for development only (the values
schema refuses quickstart.enabled unless dev: true). Object names are fixed so that values-quickstart.yaml can
refer to them: kerf-postgres, kerf-vm (VictoriaMetrics), kerf-ksm (kube-state-metrics), kerf-quickstart-db,
kerf-quickstart-admin, kerf-quickstart-scenario, kerf-quickstart-history.
*/ -}}

{{- define "qs.labels" -}}
app.kubernetes.io/part-of: kerf
app.kubernetes.io/managed-by: {{ .ctx.Release.Service }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/name: {{ .name }}
kerf.getkerf.dev/dev-only: "true"
{{- end -}}

{{- define "qs.selector" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
{{- end -}}

{{- /* A random value kept across upgrades: the value of key in Secret name when it exists, else a new one. */ -}}
{{- define "qs.stableSecret" -}}
{{- $existing := lookup "v1" "Secret" .ctx.Release.Namespace .secret -}}
{{- if and $existing $existing.data (index $existing.data .key) -}}
{{- index $existing.data .key | b64dec -}}
{{- else -}}
{{- randAlphaNum 24 -}}
{{- end -}}
{{- end -}}

{{- /* Image of a Kerf component of this release (the same rule as the component charts' "<name>.c.image"). */ -}}
{{- define "qs.image" -}}
{{- $img := (index .ctx.Values .component).image -}}
{{- $reg := $img.registry | default (dig "imageRegistry" "" .ctx.Values.global) -}}
{{- $repo := $img.repository -}}
{{- if $reg }}{{ $repo = printf "%s/%s" (trimSuffix "/" $reg) $repo }}{{ end -}}
{{- printf "%s:%s" $repo (toString ($img.tag | default .ctx.Chart.AppVersion)) -}}
{{- end -}}

{{- define "qs.securityContext" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
capabilities:
  drop: [ALL]
{{- end -}}
