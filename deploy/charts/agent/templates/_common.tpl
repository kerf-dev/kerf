{{- /*
Shared helpers of the Kerf component charts (WO-18b).

The master copy lives in deploy/charts/hub/templates/_common.tpl; `make charts-sync` copies it
into web, pricing, agent and webhook with the "agent." prefix replaced by the chart's own, and
`make charts-lint` plus deploy/charts/sync_test.go fail when a copy drifts. Edit the master only.

The prefix keeps the template names unique per chart: an umbrella chart loads every subchart's
templates into one namespace, where equal names would silently override each other.

Every helper takes the chart's root context unless it says otherwise and relies on these
chart-specific helpers: "agent.fullname", "agent.labels", "agent.selectorLabels".
*/ -}}

{{- /* Image reference: [registry/]repository:tag[@digest]. image.registry wins over
global.imageRegistry; both empty = repository as given (it may hold the registry itself). */ -}}
{{- define "agent.c.image" -}}
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
{{- include "agent.c.imagePullSecrets" (dict "ctx" . "indent" 6) }} */ -}}
{{- define "agent.c.imagePullSecrets" -}}
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
{{- define "agent.c.replicas" -}}
{{- if and (hasKey .Values "replicas") (not (kindIs "invalid" .Values.replicas)) -}}
{{- .Values.replicas | int -}}
{{- else -}}
{{- .Values.replicaCount | default 1 | int -}}
{{- end -}}
{{- end -}}

{{- /* "true" when more than one pod can run (replicas > 1 or an HPA with minReplicas > 1). */ -}}
{{- define "agent.c.multiReplica" -}}
{{- $as := .Values.autoscaling | default dict -}}
{{- if or (gt (include "agent.c.replicas" . | int) 1) (and $as.enabled (gt ($as.minReplicas | default 1 | int) 1)) -}}true{{- end -}}
{{- end -}}

{{- /* topologySpreadConstraints at the given indent: the explicit list, else the default node and
zone spread (topologySpread.enabled) when more than one pod can run. */ -}}
{{- define "agent.c.topologySpread" -}}
{{- $ctx := .ctx -}}
{{- $ts := $ctx.Values.topologySpread | default dict -}}
{{- if $ctx.Values.topologySpreadConstraints }}
{{ printf "topologySpreadConstraints:" | indent .indent }}
{{ toYaml $ctx.Values.topologySpreadConstraints | indent (add .indent 2 | int) }}
{{- else if and $ts.enabled (include "agent.c.multiReplica" $ctx) }}
{{ printf "topologySpreadConstraints:" | indent .indent }}
{{- range list "kubernetes.io/hostname" "topology.kubernetes.io/zone" }}
{{ printf "- maxSkew: %d" ($ts.maxSkew | default 1 | int) | indent (add $.indent 2 | int) }}
{{ printf "topologyKey: %s" . | indent (add $.indent 4 | int) }}
{{ printf "whenUnsatisfiable: %s" ($ts.whenUnsatisfiable | default "ScheduleAnyway") | indent (add $.indent 4 | int) }}
{{ printf "labelSelector:" | indent (add $.indent 4 | int) }}
{{ printf "matchLabels:" | indent (add $.indent 6 | int) }}
{{ include "agent.selectorLabels" $ctx | indent (add $.indent 8 | int) }}
{{- end }}
{{- end -}}
{{- end -}}

{{- /* PodDisruptionBudget, when podDisruptionBudget.enabled and more than one pod can run.
maxUnavailable wins over minAvailable when both are set. */ -}}
{{- define "agent.c.pdb" -}}
{{- $pdb := .Values.podDisruptionBudget | default dict -}}
{{- if and $pdb.enabled (include "agent.c.multiReplica" .) }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "agent.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels: {{- include "agent.labels" . | nindent 4 }}
spec:
  {{- if not (kindIs "invalid" $pdb.maxUnavailable) }}
  maxUnavailable: {{ $pdb.maxUnavailable }}
  {{- else }}
  minAvailable: {{ $pdb.minAvailable | default 1 }}
  {{- end }}
  selector:
    matchLabels: {{- include "agent.selectorLabels" . | nindent 6 }}
{{- end }}
{{- end -}}

{{- /* HorizontalPodAutoscaler on CPU and/or memory utilization (autoscaling.enabled). */ -}}
{{- define "agent.c.hpa" -}}
{{- $as := .Values.autoscaling | default dict -}}
{{- if $as.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: {{ include "agent.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels: {{- include "agent.labels" . | nindent 4 }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: {{ include "agent.fullname" . }}
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
{{- define "agent.c.serviceMonitor" -}}
{{- $ctx := .ctx -}}
{{- $sm := $ctx.Values.serviceMonitor | default dict -}}
{{- if $sm.enabled }}
{{- $kind := $sm.kind | default "ServiceMonitor" -}}
{{- if not (has $kind (list "ServiceMonitor" "VMServiceScrape")) }}{{ fail "serviceMonitor.kind must be ServiceMonitor or VMServiceScrape" }}{{ end }}
apiVersion: {{ ternary "monitoring.coreos.com/v1" "operator.victoriametrics.com/v1beta1" (eq $kind "ServiceMonitor") }}
kind: {{ $kind }}
metadata:
  name: {{ include "agent.fullname" $ctx }}
  namespace: {{ $sm.namespace | default $ctx.Release.Namespace }}
  labels:
    {{- include "agent.labels" $ctx | nindent 4 }}
    {{- with $sm.labels }}{{ toYaml . | nindent 4 }}{{ end }}
spec:
  selector:
    matchLabels: {{- .selector | default (include "agent.selectorLabels" $ctx | fromYaml) | toYaml | nindent 6 }}
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
{{- define "agent.c.secretStoreRef" -}}
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
{{- define "agent.c.externalSecret" -}}
{{- $ctx := .ctx -}}
{{- $es := .es -}}
{{- $eso := $ctx.Values.externalSecrets | default dict -}}
{{- $remote := $es.remoteRef | default dict -}}
apiVersion: {{ $eso.apiVersion | default "external-secrets.io/v1" }}
kind: ExternalSecret
metadata:
  name: {{ .name }}
  namespace: {{ $ctx.Release.Namespace }}
  labels: {{- include "agent.labels" $ctx | nindent 4 }}
  {{- if .keep }}
  annotations:
    helm.sh/resource-policy: keep
  {{- end }}
spec:
  refreshInterval: {{ $es.refreshInterval | default $eso.refreshInterval | default "1h" }}
  secretStoreRef: {{- include "agent.c.secretStoreRef" (dict "ctx" $ctx "es" $es) | nindent 4 }}
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
{{- define "agent.c.egressRules" -}}
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
{{- define "agent.c.durationSeconds" -}}
{{- $s := 0.0 -}}
{{- range regexFindAll "[0-9.]+(ns|us|µs|ms|h|m|s)" (toString .) -1 -}}
{{- $n := regexFind "[0-9.]+" . | float64 -}}
{{- if regexMatch "(ns|us|µs|ms)$" . }}{{/* below a second */}}
{{- else if hasSuffix "h" . }}{{ $s = addf $s (mulf $n 3600) }}{{ else if hasSuffix "m" . }}{{ $s = addf $s (mulf $n 60) }}{{ else }}{{ $s = addf $s $n }}{{ end -}}
{{- end -}}
{{- $s | int -}}
{{- end -}}

{{- /* ---- Umbrella chart (WO-18d): enable gate, names and wiring --------------------------------

Every template file of every component chart is wrapped in
  {{- if include "agent.c.enabled" . -}} ... {{ end -}}
Helm evaluates a dependency's `condition:` before any template and cannot derive it from another
value, so the umbrella chart deploy/charts/kerf selects its components here instead:

- `enabled` set (true/false, umbrella `<component>.enabled`) wins;
- else global.mode (umbrella `mode`): hub = hub, web, pricing; spoke = agent, webhook; all = every one;
- global.mode empty (a component chart installed on its own, as in development): enabled, and
  nothing below runs, so standalone renders are unchanged (`make charts-diff-dev`).

With global.mode set, the gate also fills in what the umbrella derives (only values left empty):
clusterName from global.clusterName; web hub.upstream, pricing hub.url (and its token Secret name)
and, in mode all, agent hub.url from the hub Service of the release. It runs at the top of every
template file, before anything reads those values. */ -}}
{{- define "agent.c.enabled" -}}
{{- $mode := dig "mode" "" (.Values.global | default dict) | toString -}}
{{- $on := true -}}
{{- if and (hasKey .Values "enabled") (not (kindIs "invalid" .Values.enabled)) -}}
{{- $on = .Values.enabled -}}
{{- else if $mode -}}
{{- $modes := dict "hub" (list "hub" "all") "web" (list "hub" "all") "pricing" (list "hub" "all") "agent" (list "spoke" "all") "webhook" (list "spoke" "all") -}}
{{- $on = has $mode (get $modes .Chart.Name | default list) -}}
{{- end -}}
{{- if $on -}}
{{- if $mode }}{{ include "agent.c.umbrellaWiring" . }}{{ end -}}
true
{{- end -}}
{{- end -}}

{{- /* Name of a component's objects in the umbrella chart: `<release>-<component>`, or plain
`<component>` when the release is named kerf. The agent always keeps the release
prefix (`kerf-agent`): it owns cluster-scoped RBAC, where a bare `agent` could clash.
`<component>.fullnameOverride` wins. dict: ctx, component. */ -}}
{{- define "agent.c.umbrellaName" -}}
{{- $rel := .ctx.Release.Name -}}
{{- if and (eq $rel "kerf") (ne .component "agent") -}}{{ .component }}
{{- else -}}{{ printf "%s-%s" $rel .component | trunc 63 | trimSuffix "-" }}
{{- end -}}
{{- end -}}

{{- /* Values the umbrella derives (see "agent.c.enabled"); prints nothing. */ -}}
{{- define "agent.c.umbrellaWiring" -}}
{{- $g := .Values.global | default dict -}}
{{- $hub := printf "%s.%s.svc.cluster.local" (include "agent.c.umbrellaName" (dict "ctx" . "component" "hub")) .Release.Namespace -}}
{{- if and $g.clusterName (not .Values.clusterName) }}{{ $_ := set .Values "clusterName" $g.clusterName }}{{ end -}}
{{- if eq .Chart.Name "web" -}}
{{- if not .Values.hub.upstream }}{{ $_ := set .Values.hub "upstream" (printf "%s:8080" $hub) }}{{ end -}}
{{- else if eq .Chart.Name "pricing" -}}
{{- if not .Values.hub.url }}{{ $_ := set .Values.hub "url" (printf "http://%s:8080" $hub) }}{{ end -}}
{{- if not .Values.hub.tokenSecret.name }}{{ $_ := set .Values.hub.tokenSecret "name" (printf "%s-hub-token" (include "agent.c.umbrellaName" (dict "ctx" . "component" "pricing"))) }}{{ end -}}
{{- else if eq .Chart.Name "agent" -}}
{{- if and (not .Values.hub.url) (not .Values.stubHub.enabled) -}}
{{- if eq $g.mode "all" }}{{ $_ := set .Values.hub "url" (printf "http://%s:8080" $hub) }}
{{- else }}{{ fail "agent.hub.url is required in mode spoke: the hub URL as this cluster reaches it, e.g. https://hub.example.internal" }}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
