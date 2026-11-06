{{- /*
Object names inside this chart. The component charts name their objects after the release
(release "hub" -> Deployment "hub"), which would make the five components of one release collide.
These definitions replace the components' "<name>.fullname": Helm parses a parent chart's templates
after its subcharts', so a parent's `define` of the same name wins (one template namespace).

  release kerf : hub, web, pricing, webhook, kerf-agent
  any other release, e.g. ro : ro-hub, ro-web, ro-pricing, ro-webhook, ro-agent

`<component>.fullnameOverride` replaces the name of one component. The rule itself is
"<name>.c.umbrellaName" in the components' _common.tpl (the gate needs it to find the hub Service).
These names equal the component charts' dev release names, so moving from those releases keeps
every object name (docs/install/eks.md, "Upgrade").
*/ -}}
{{- define "hub.fullname" -}}{{ .Values.fullnameOverride | default (include "hub.c.umbrellaName" (dict "ctx" . "component" "hub")) }}{{- end -}}
{{- define "web.fullname" -}}{{ .Values.fullnameOverride | default (include "web.c.umbrellaName" (dict "ctx" . "component" "web")) }}{{- end -}}
{{- define "pricing.fullname" -}}{{ .Values.fullnameOverride | default (include "pricing.c.umbrellaName" (dict "ctx" . "component" "pricing")) }}{{- end -}}
{{- define "agent.fullname" -}}{{ .Values.fullnameOverride | default (include "agent.c.umbrellaName" (dict "ctx" . "component" "agent")) }}{{- end -}}
{{- define "webhook.fullname" -}}{{ .Values.fullnameOverride | default (include "webhook.c.umbrellaName" (dict "ctx" . "component" "webhook")) }}{{- end -}}

{{- /* Name of a component of this release (umbrella context). */ -}}
{{- define "ro.name" -}}
{{- $v := index .ctx.Values .component | default dict -}}
{{- $v.fullnameOverride | default (include "hub.c.umbrellaName" (dict "ctx" .ctx "component" .component)) -}}
{{- end -}}

{{- /* "true" when a component is installed by this release (the same rule as "<name>.c.enabled"). */ -}}
{{- define "ro.enabled" -}}
{{- $v := index .ctx.Values .component | default dict -}}
{{- if and (hasKey $v "enabled") (not (kindIs "invalid" $v.enabled)) -}}
{{- if $v.enabled }}true{{ end -}}
{{- else -}}
{{- $modes := dict "hub" (list "hub" "all") "web" (list "hub" "all") "pricing" (list "hub" "all") "agent" (list "spoke" "all") "webhook" (list "spoke" "all") -}}
{{- if has (toString .ctx.Values.global.mode) (get $modes .component) }}true{{ end -}}
{{- end -}}
{{- end -}}
