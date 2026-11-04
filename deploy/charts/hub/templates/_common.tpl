{{- /*
Shared helpers of the resource-optimizer component charts (WO-18b).

The master copy lives in deploy/charts/hub/templates/_common.tpl; `make charts-sync` copies it
into web, pricing, agent and webhook with the "hub." prefix replaced by the chart's own, and
`make charts-lint` plus deploy/charts/sync_test.go fail when a copy drifts. Edit the master only.

The prefix keeps the template names unique per chart: an umbrella chart loads every subchart's
templates into one namespace, where equal names would silently override each other.

Every helper takes the chart's root context unless it says otherwise and relies on these
chart-specific helpers: "hub.fullname", "hub.labels", "hub.selectorLabels".
*/ -}}

{{- /* Image reference: [registry/]repository:tag[@digest]. image.registry wins over
global.imageRegistry; both empty = repository as given (it may hold the registry itself). */ -}}
{{- define "hub.c.image" -}}
{{- $img := .Values.image -}}
{{- $reg := $img.registry | default (dig "imageRegistry" "" (.Values.global | default dict)) -}}
{{- $repo := $img.repository -}}
{{- if $reg }}{{ $repo = printf "%s/%s" (trimSuffix "/" $reg) $repo }}{{ end -}}
{{- $tag := toString ($img.tag | default .Chart.AppVersion) -}}
{{- $ref := printf "%s:%s" $repo $tag -}}
{{- with $img.digest }}{{ $ref = printf "%s@%s" $ref . }}{{ end -}}
{{- $ref -}}
{{- end -}}

{{- /* imagePullSecrets (global.imagePullSecrets + imagePullSecrets; entries are names or {name}),
rendered as a pod spec field at the given indent, or nothing. Usage:
{{- include "hub.c.imagePullSecrets" (dict "ctx" . "indent" 6) }} */ -}}
{{- define "hub.c.imagePullSecrets" -}}
{{- $ctx := .ctx -}}
{{- $all := concat (dig "imagePullSecrets" (list) ($ctx.Values.global | default dict)) ($ctx.Values.imagePullSecrets | default list) -}}
{{- $names := list -}}
{{- range $all -}}
{{- if kindIs "string" . }}{{ $names = append $names . }}{{ else if .name }}{{ $names = append $names .name }}{{ end -}}
{{- end -}}
{{- if $names }}
{{ printf "imagePullSecrets:" | indent .indent }}
{{- range uniq $names }}
{{ printf "- name: %s" . | indent (add $.indent 2 | int) }}
{{- end }}
{{- end -}}
{{- end -}}

{{- /* Replica count: replicaCount, or the deprecated alias `replicas` when the chart has one and it
is set (non-null). */ -}}
{{- define "hub.c.replicas" -}}
{{- if and (hasKey .Values "replicas") (not (kindIs "invalid" .Values.replicas)) -}}
{{- .Values.replicas | int -}}
{{- else -}}
{{- .Values.replicaCount | default 1 | int -}}
{{- end -}}
{{- end -}}

{{- /* "true" when more than one pod can run (replicas > 1 or an HPA with minReplicas > 1). */ -}}
{{- define "hub.c.multiReplica" -}}
{{- $as := .Values.autoscaling | default dict -}}
{{- if or (gt (include "hub.c.replicas" . | int) 1) (and $as.enabled (gt ($as.minReplicas | default 1 | int) 1)) -}}true{{- end -}}
{{- end -}}

{{- /* topologySpreadConstraints at the given indent: the explicit list, else the default node and
zone spread (topologySpread.enabled) when more than one pod can run. */ -}}
{{- define "hub.c.topologySpread" -}}
{{- $ctx := .ctx -}}
{{- $ts := $ctx.Values.topologySpread | default dict -}}
{{- if $ctx.Values.topologySpreadConstraints }}
{{ printf "topologySpreadConstraints:" | indent .indent }}
{{ toYaml $ctx.Values.topologySpreadConstraints | indent (add .indent 2 | int) }}
{{- else if and $ts.enabled (include "hub.c.multiReplica" $ctx) }}
{{ printf "topologySpreadConstraints:" | indent .indent }}
{{- range list "kubernetes.io/hostname" "topology.kubernetes.io/zone" }}
{{ printf "- maxSkew: %d" ($ts.maxSkew | default 1 | int) | indent (add $.indent 2 | int) }}
{{ printf "topologyKey: %s" . | indent (add $.indent 4 | int) }}
{{ printf "whenUnsatisfiable: %s" ($ts.whenUnsatisfiable | default "ScheduleAnyway") | indent (add $.indent 4 | int) }}
{{ printf "labelSelector:" | indent (add $.indent 4 | int) }}
{{ printf "matchLabels:" | indent (add $.indent 6 | int) }}
{{ include "hub.selectorLabels" $ctx | indent (add $.indent 8 | int) }}
{{- end }}
{{- end -}}
{{- end -}}

{{- /* PodDisruptionBudget, when podDisruptionBudget.enabled and more than one pod can run.
maxUnavailable wins over minAvailable when both are set. */ -}}
{{- define "hub.c.pdb" -}}
{{- $pdb := .Values.podDisruptionBudget | default dict -}}
{{- if and $pdb.enabled (include "hub.c.multiReplica" .) }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "hub.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels: {{- include "hub.labels" . | nindent 4 }}
spec:
  {{- if not (kindIs "invalid" $pdb.maxUnavailable) }}
  maxUnavailable: {{ $pdb.maxUnavailable }}
  {{- else }}
  minAvailable: {{ $pdb.minAvailable | default 1 }}
  {{- end }}
  selector:
    matchLabels: {{- include "hub.selectorLabels" . | nindent 6 }}
{{- end }}
{{- end -}}

{{- /* HorizontalPodAutoscaler on CPU and/or memory utilization (autoscaling.enabled). */ -}}
{{- define "hub.c.hpa" -}}
{{- $as := .Values.autoscaling | default dict -}}
{{- if $as.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "hub.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels: {{- include "hub.labels" . | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "hub.fullname" . }}
  minReplicas: {{ $as.minReplicas | default 1 }}
  maxReplicas: {{ required "autoscaling.maxReplicas is required" $as.maxReplicas }}
  metrics:
    {{- with $as.targetCPUUtilizationPercentage }}
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: {{ . }}
    {{- end }}
    {{- with $as.targetMemoryUtilizationPercentage }}
    - type: Resource
      resource:
        name: memory
        target:
          type: Utilization
          averageUtilization: {{ . }}
    {{- end }}
  {{- with $as.behavior }}
  behavior: {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
{{- end -}}

{{- /* ServiceMonitor (Prometheus Operator) or VMServiceScrape (VictoriaMetrics operator) for the
chart's metrics Service. dict: ctx, port (Service port name), path (default /metrics),
selector (labels of the Service; default the chart's selector labels). */ -}}
{{- define "hub.c.serviceMonitor" -}}
{{- $ctx := .ctx -}}
{{- $sm := $ctx.Values.serviceMonitor | default dict -}}
{{- if $sm.enabled }}
{{- $kind := $sm.kind | default "ServiceMonitor" -}}
{{- if not (has $kind (list "ServiceMonitor" "VMServiceScrape")) }}{{ fail "serviceMonitor.kind must be ServiceMonitor or VMServiceScrape" }}{{ end }}
apiVersion: {{ ternary "monitoring.coreos.com/v1" "operator.victoriametrics.com/v1beta1" (eq $kind "ServiceMonitor") }}
kind: {{ $kind }}
metadata:
  name: {{ include "hub.fullname" $ctx }}
  namespace: {{ $sm.namespace | default $ctx.Release.Namespace }}
  labels:
    {{- include "hub.labels" $ctx | nindent 4 }}
    {{- with $sm.labels }}{{ toYaml . | nindent 4 }}{{ end }}
spec:
  selector:
    matchLabels: {{- .selector | default (include "hub.selectorLabels" $ctx | fromYaml) | toYaml | nindent 6 }}
  namespaceSelector:
    matchNames: [{{ $ctx.Release.Namespace | quote }}]
  endpoints:
    - port: {{ .port }}
      path: {{ .path | default "/metrics" }}
      {{- with $sm.interval }}
      interval: {{ . }}
      {{- end }}
      {{- with $sm.scrapeTimeout }}
      scrapeTimeout: {{ . }}
      {{- end }}
      {{- /* Hub-side series set `cluster` themselves (docs/contracts/metrics.md). */}}
      honorLabels: {{ $sm.honorLabels | default false }}
      {{- with $sm.relabelings }}
      {{ ternary "relabelings" "relabelConfigs" (eq $kind "ServiceMonitor") }}: {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with $sm.metricRelabelings }}
      {{ ternary "metricRelabelings" "metricRelabelConfigs" (eq $kind "ServiceMonitor") }}: {{- toYaml . | nindent 8 }}
      {{- end }}
{{- end }}
{{- end -}}

{{- /* The SecretStore an ExternalSecret reads from: the secret's own externalSecret.secretStoreRef,
else externalSecrets.secretStoreRef, else global.externalSecrets.secretStoreRef. dict: ctx, es. */ -}}
{{- define "hub.c.secretStoreRef" -}}
{{- $g := dig "externalSecrets" "secretStoreRef" dict (.ctx.Values.global | default dict) -}}
{{- $c := dig "secretStoreRef" dict (.ctx.Values.externalSecrets | default dict) -}}
{{- $ref := mergeOverwrite (dict "kind" "ClusterSecretStore") $g $c (.es.secretStoreRef | default dict) -}}
{{- if not $ref.name }}{{ fail "externalSecrets.secretStoreRef.name (or global.externalSecrets.secretStoreRef.name) is required when an externalSecret is enabled" }}{{ end -}}
{{- toYaml $ref -}}
{{- end -}}

{{- /* ExternalSecret (External Secrets Operator) that creates Secret `name`.
dict: ctx, name, es (the secret's externalSecret values: remoteRef, secretStoreRef, refreshInterval),
data (list of {secretKey, property}: one entry per Secret key, read from es.remoteRef.key),
template (optional map of Secret key -> ESO template), keep (bool: helm.sh/resource-policy: keep). */ -}}
{{- define "hub.c.externalSecret" -}}
{{- $ctx := .ctx -}}
{{- $es := .es -}}
{{- $eso := $ctx.Values.externalSecrets | default dict -}}
{{- $remote := $es.remoteRef | default dict -}}
apiVersion: {{ $eso.apiVersion | default "external-secrets.io/v1" }}
kind: ExternalSecret
metadata:
  name: {{ .name }}
  namespace: {{ $ctx.Release.Namespace }}
  labels: {{- include "hub.labels" $ctx | nindent 4 }}
  {{- if .keep }}
  annotations:
    helm.sh/resource-policy: keep
  {{- end }}
spec:
  refreshInterval: {{ $es.refreshInterval | default $eso.refreshInterval | default "1h" }}
  secretStoreRef: {{- include "hub.c.secretStoreRef" (dict "ctx" $ctx "es" $es) | nindent 4 }}
  target:
    name: {{ .name }}
    creationPolicy: Owner
    {{- with .template }}
    template:
      engineVersion: v2
      type: Opaque
      data: {{- toYaml . | nindent 8 }}
    {{- end }}
  data:
    {{- range .data }}
    - secretKey: {{ .secretKey }}
      remoteRef:
        key: {{ required (printf "externalSecret.remoteRef.key is required for Secret %s" $.name) $remote.key }}
        {{- with (.property | default $remote.property) }}
        property: {{ . }}
        {{- end }}
        {{- with $remote.version }}
        version: {{ . }}
        {{- end }}
    {{- end }}
{{- end -}}

{{- /* NetworkPolicy egress rules shared by every chart (at indent 4 under `egress:`): DNS, TCP
`ports` to any destination, then the raw rules in networkPolicy.egress.to. dict: ctx, ports. */ -}}
{{- define "hub.c.egressRules" -}}
{{- $np := .ctx.Values.networkPolicy }}
    - ports:
        - {protocol: UDP, port: 53}
        - {protocol: TCP, port: 53}
    {{- with .ports }}
    - ports:
        {{- range . }}
        - {protocol: TCP, port: {{ . }}}
        {{- end }}
    {{- end }}
    {{- with $np.egress.to }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
{{- end -}}

{{- /* Seconds in a Go duration ("24h", "1h30m", "90s"; ms and smaller are ignored). */ -}}
{{- define "hub.c.durationSeconds" -}}
{{- $s := 0.0 -}}
{{- range regexFindAll "[0-9.]+(ns|us|µs|ms|h|m|s)" (toString .) -1 -}}
{{- $n := regexFind "[0-9.]+" . | float64 -}}
{{- if regexMatch "(ns|us|µs|ms)$" . }}{{/* below a second */}}
{{- else if hasSuffix "h" . }}{{ $s = addf $s (mulf $n 3600) }}{{ else if hasSuffix "m" . }}{{ $s = addf $s (mulf $n 60) }}{{ else }}{{ $s = addf $s $n }}{{ end -}}
{{- end -}}
{{- $s | int -}}
{{- end -}}
