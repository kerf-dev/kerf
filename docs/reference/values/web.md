<!-- Generated from deploy/charts/web/values.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# `web` values (web UI)

The values of the `web` component chart. In the `kerf` chart they sit under `web:`; a key listed here as `image.tag` is then `web.image.tag`. Nested keys appear after their parent, with the parent's default shown in full or abbreviated.

| Key | Type | Default | Description |
|---|---|---|---|
| `global` | object | see the keys below | Set by an umbrella chart; read by every kerf chart. |
| `global.imageRegistry` | string | `""` | prefixed to image.repository; image.registry wins over it |
| `global.imagePullSecrets` | array | `[]` | names or {name: ...} |
| `global.externalSecrets` | object | see the keys below |  |
| `global.externalSecrets.secretStoreRef` | object | `{}` | fallback SecretStore for every ExternalSecret |
| `image` | object | see the keys below |  |
| `image.registry` | string | `""` |  |
| `image.repository` | string | `localhost:5005/kerf/web` |  |
| `image.tag` | string | `dev` |  |
| `image.digest` | string | `""` | sha256:..., appended as @digest |
| `image.pullPolicy` | string | `IfNotPresent` |  |
| `imagePullSecrets` | array | `[]` |  |
| `clusterName` | string | `""` | Set by Tilt (unused: the UI reads every cluster through the hub). |
| `replicaCount` | integer | `2` |  |
| `podDisruptionBudget` | object | see the keys below | Created when more than one pod can run (replicaCount > 1 or autoscaling.minReplicas > 1). |
| `podDisruptionBudget.enabled` | boolean | `true` |  |
| `podDisruptionBudget.minAvailable` | integer | `1` |  |
| `podDisruptionBudget.maxUnavailable` | object | `null` |  |
| `autoscaling` | object | see the keys below |  |
| `autoscaling.enabled` | boolean | `false` |  |
| `autoscaling.minReplicas` | integer | `2` |  |
| `autoscaling.maxReplicas` | integer | `6` |  |
| `autoscaling.targetCPUUtilizationPercentage` | integer | `75` |  |
| `autoscaling.targetMemoryUtilizationPercentage` | object | `null` |  |
| `autoscaling.behavior` | object | `{}` |  |
| `topologySpread` | object | see the keys below | Default node and zone spread (ScheduleAnyway) when more than one pod can run; topologySpreadConstraints replaces it. |
| `topologySpread.enabled` | boolean | `true` |  |
| `topologySpread.whenUnsatisfiable` | string | `ScheduleAnyway` |  |
| `topologySpreadConstraints` | array | `[]` |  |
| `priorityClassName` | string | `""` |  |
| `serviceAccount` | object | see the keys below | The UI needs no Kubernetes or AWS access. create: false uses the namespace's default ServiceAccount (the token is never mounted either way). |
| `serviceAccount.create` | boolean | `false` |  |
| `serviceAccount.name` | string | `""` |  |
| `serviceAccount.annotations` | object | `{}` |  |
| `serviceAccount.automountServiceAccountToken` | boolean | `false` |  |
| `containerPort` | integer | `8080` | Port nginx listens on. The image listens on 80 today (root, NET_BIND_SERVICE); a non-root image listens on 8080: set containerPort: 8080 together with the non-root securityContext below. |
| `preStopSleepSeconds` | integer | `5` | Seconds nginx keeps serving after the pod is marked terminating, so the ingress controller removes it from its endpoints first (no 502s during a rolling update). Built-in preStop sleep action. 0 = off. |
| `podSecurityContext` | object | see the keys below | The image runs nginx as user 101 on 8080 with a read-only root filesystem. |
| `podSecurityContext.runAsNonRoot` | boolean | `true` |  |
| `podSecurityContext.runAsUser` | integer | `101` |  |
| `podSecurityContext.runAsGroup` | integer | `101` |  |
| `podSecurityContext.fsGroup` | integer | `101` |  |
| `podSecurityContext.seccompProfile` | object | see the keys below |  |
| `podSecurityContext.seccompProfile.type` | string | `RuntimeDefault` |  |
| `securityContext` | object | see the keys below | eadOnlyRootFilesystem: true mounts emptyDirs on nginx's writable paths (readOnlyPaths below). |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` |  |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` |  |
| `securityContext.capabilities` | object | see the keys below |  |
| `securityContext.capabilities.drop` | array | `["ALL"]` |  |
| `readOnlyPaths` | array | `["/var/cache/nginx", "/var/run", "/tmp", "/etc/nginx/conf.d"]` | emptyDirs mounted when securityContext.readOnlyRootFilesystem is true: temp and cache (/var/cache/nginx), the pid file (/var/run), /tmp and the config rendered from templates at start (/etc/nginx/conf.d). |
| `podLabels` | object | `{}` |  |
| `extraEnv` | array | `[]` |  |
| `extraEnvFrom` | array | `[]` |  |
| `extraVolumes` | array | `[]` |  |
| `extraVolumeMounts` | array | `[]` |  |
| `hub` | object | see the keys below |  |
| `hub.upstream` | string | `hub.kerf.svc.cluster.local:8080` | host:port of the hub API Service (the hub chart names it after its release, `hub`). nginx resolves it per request with the cluster DNS, which does not apply search domains: use the full name. |
| `trustedProxies` | array | `[]` | Load balancers or ingress controllers in front of the UI (CIDRs or addresses, e.g. the VPC subnets of the ALB). nginx then takes the client address from the X-Forwarded-For entry they add, and the hub rate-limits failed sign-ins per real client. Empty: the client is the connecting address (the proxy's, when there is one). |
| `service` | object | see the keys below |  |
| `service.type` | string | `ClusterIP` |  |
| `service.port` | integer | `80` |  |
| `service.nodePort` | object | `null` | e.g. 30090 with type NodePort |
| `service.annotations` | object | `{}` |  |
| `ingress` | object | see the keys below | The UI for browsers. AWS Load Balancer Controller example (: scheme of your choice, ACM):<br>`className: alb`<br>`annotations:`<br>`alb.ingress.kubernetes.io/scheme: internal          # or internet-facing`<br>`alb.ingress.kubernetes.io/target-type: ip`<br>`alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'`<br>`alb.ingress.kubernetes.io/ssl-redirect: "443"`<br>`alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:<region>:<account>:certificate/<id>`<br>`alb.ingress.kubernetes.io/healthcheck-path: /healthz`<br>`hosts: [{host: ro.example.com, paths: [{path: /, pathType: Prefix}]}]`<br>`tls: [{hosts: [ro.example.com]}]` |
| `ingress.enabled` | boolean | `false` |  |
| `ingress.className` | string | `""` |  |
| `ingress.annotations` | object | `{}` |  |
| `ingress.hosts` | array | `[]` |  |
| `ingress.tls` | array | `[]` |  |
| `networkPolicy` | object | see the keys below | NetworkPolicy. Ingress to the nginx port from `ingress.from` (NetworkPolicyPeers; [] = any source: the ALB or ingress controller). Egress: DNS and TCP `egress.ports` (the hub) to any destination, plus the raw rules in `egress.to`. |
| `networkPolicy.enabled` | boolean | `false` |  |
| `networkPolicy.ingress` | object | see the keys below |  |
| `networkPolicy.ingress.from` | array | `[]` |  |
| `networkPolicy.egress` | object | see the keys below |  |
| `networkPolicy.egress.enabled` | boolean | `true` |  |
| `networkPolicy.egress.ports` | array | `[8080, 443]` |  |
| `networkPolicy.egress.to` | array | `[]` |  |
| `resources` | object | see the keys below |  |
| `resources.requests` | object | see the keys below |  |
| `resources.requests.cpu` | string | `10m` |  |
| `resources.requests.memory` | string | `32Mi` |  |
| `resources.limits` | object | see the keys below |  |
| `resources.limits.memory` | string | `128Mi` |  |
| `podAnnotations` | object | `{}` |  |
| `nodeSelector` | object | `{}` |  |
| `tolerations` | array | `[]` |  |
| `affinity` | object | `{}` |  |
