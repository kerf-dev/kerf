<!-- Generated from deploy/charts/hub/values.yaml by tools/docs. Do not edit: change the source and run `make docs-generate`. -->

# `hub` values (API, recommender and readiness checks)

The values of the `hub` component chart. In the `kerf` chart they sit under `hub:`; a key listed here as `image.tag` is then `hub.image.tag`. Nested keys appear after their parent, with the parent's default shown in full or abbreviated.

| Key | Type | Default | Description |
|---|---|---|---|
| `global` | object | see the keys below | Set by an umbrella chart; read by every kerf chart. |
| `global.imageRegistry` | string | `""` | Registry prefixed to image.repository (e.g. 123456789012.dkr.ecr.eu-west-1.amazonaws.com); image.registry wins over it. |
| `global.imagePullSecrets` | array | `[]` | Pull secrets added to every pod (names or {name: ...}). |
| `global.externalSecrets` | object | see the keys below | Fallback SecretStore for every ExternalSecret: {kind: ClusterSecretStore, name: ...}. |
| `global.externalSecrets.secretStoreRef` | object | `{}` |  |
| `image` | object | see the keys below |  |
| `image.registry` | string | `""` | Optional registry; empty = repository holds the full reference. |
| `image.repository` | string | `localhost:5005/kerf/hub` |  |
| `image.tag` | string | `dev` |  |
| `image.digest` | string | `""` | sha256:... appended as repository:tag@digest (pins the image). |
| `image.pullPolicy` | string | `IfNotPresent` |  |
| `imagePullSecrets` | array | `[]` |  |
| `clusterName` | string | `""` | Set by Tilt (unused by the hub, which serves every cluster). |
| `replicaCount` | integer | `2` | The hub elects a leader on Postgres: only the leader runs the recommender, readiness checks and sweeps; every replica serves the API. The database URL must reach Postgres directly or through a session-mode pooler (the leader lock and LISTEN need a real session). |
| `updateStrategy` | object | see the keys below |  |
| `updateStrategy.type` | string | `RollingUpdate` |  |
| `updateStrategy.rollingUpdate` | object | see the keys below |  |
| `updateStrategy.rollingUpdate.maxUnavailable` | integer | `0` |  |
| `updateStrategy.rollingUpdate.maxSurge` | integer | `1` |  |
| `terminationGracePeriodSeconds` | integer | `30` | preStop sleep (5 s) + leader stop (<= 15 s) + HTTP drain (10 s) on SIGTERM; keep >= 30. |
| `preStopSleepSeconds` | integer | `5` | Seconds the pod keeps serving after it is marked terminating, so the ingress controller and kube-proxy remove it from their endpoints before the hub stops accepting connections (no 502s during a rolling update). Uses the built-in preStop sleep action (no shell in the image). 0 = off. |
| `podDisruptionBudget` | object | see the keys below | Created only when more than one hub pod can run (replicaCount > 1 or autoscaling.minReplicas > 1). |
| `podDisruptionBudget.enabled` | boolean | `true` |  |
| `podDisruptionBudget.minAvailable` | integer | `1` |  |
| `podDisruptionBudget.maxUnavailable` | object | `null` |  |
| `autoscaling` | object | see the keys below | CPU-based HPA. Needs leader election; replicaCount is then ignored. |
| `autoscaling.enabled` | boolean | `false` |  |
| `autoscaling.minReplicas` | integer | `2` |  |
| `autoscaling.maxReplicas` | integer | `4` |  |
| `autoscaling.targetCPUUtilizationPercentage` | integer | `75` |  |
| `autoscaling.targetMemoryUtilizationPercentage` | object | `null` |  |
| `autoscaling.behavior` | object | `{}` |  |
| `topologySpread` | object | see the keys below | Default spread over nodes and zones (best effort: ScheduleAnyway), applied when more than one pod can run. topologySpreadConstraints replaces it with your own list. |
| `topologySpread.enabled` | boolean | `true` |  |
| `topologySpread.whenUnsatisfiable` | string | `ScheduleAnyway` |  |
| `topologySpreadConstraints` | array | `[]` |  |
| `priorityClassName` | string | `""` |  |
| `nodeSelector` | object | `{}` |  |
| `tolerations` | array | `[]` |  |
| `affinity` | object | `{}` |  |
| `serviceAccount` | object | see the keys below |  |
| `serviceAccount.create` | boolean | `true` |  |
| `serviceAccount.name` | string | `""` | Default: the release name. |
| `serviceAccount.annotations` | object | `{}` | IRSA or Pod Identity, should the hub ever need AWS access (it does not today), e.g. eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/<role |
| `serviceAccount.automountServiceAccountToken` | boolean | `false` | The hub never calls the Kubernetes API. |
| `podLabels` | object | `{}` |  |
| `podAnnotations` | object | `{}` |  |
| `extraEnv` | array | `[]` |  |
| `extraEnvFrom` | array | `[]` |  |
| `extraVolumes` | array | `[]` |  |
| `extraVolumeMounts` | array | `[]` |  |
| `service` | object | see the keys below |  |
| `service.type` | string | `ClusterIP` |  |
| `service.port` | integer | `8080` |  |
| `service.nodePort` | object | `null` | e.g. 30080 with type NodePort |
| `service.scrape` | boolean | `true` | Hub vmagent scrapes Services with these annotations (dev/README.md). |
| `service.annotations` | object | `{}` |  |
| `ingress` | object | see the keys below | Hub API for agents in other clusters, the pricing service and browsers (through the web UI). AWS Load Balancer Controller example (internal ALB, ACM certificate):<br>`className: alb`<br>`annotations:`<br>`alb.ingress.kubernetes.io/scheme: internal`<br>`alb.ingress.kubernetes.io/target-type: ip`<br>`alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'`<br>`alb.ingress.kubernetes.io/ssl-redirect: "443"`<br>`alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:<region>:<account>:certificate/<id>`<br>`alb.ingress.kubernetes.io/healthcheck-path: /readyz`<br>`alb.ingress.kubernetes.io/group.name: kerf   # share one ALB with the web UI`<br>`hosts: [{host: hub.example.internal, paths: [{path: /, pathType: Prefix}]}]`<br>`tls: [{hosts: [hub.example.internal]}]            # ALB: the certificate comes from ACM` |
| `ingress.enabled` | boolean | `false` |  |
| `ingress.className` | string | `""` |  |
| `ingress.annotations` | object | `{}` |  |
| `ingress.hosts` | array | `[]` |  |
| `ingress.tls` | array | `[]` |  |
| `database` | object | see the keys below | Postgres. One of:<br>`url                 inline URL (rendered into Secret <release>-db); development only`<br>`existingSecret      your Secret with the full URL under existingSecretKey`<br>`externalSecret      External Secrets Operator builds Secret <release>-db (see below)` |
| `database.url` | string | `""` |  |
| `database.existingSecret` | string | `""` |  |
| `database.existingSecretKey` | string | `DATABASE_URL` |  |
| `database.host` | string | `""` | Used to build the URL when externalSecret.source is rds (RDS with verify-full). |
| `database.port` | integer | `5432` |  |
| `database.name` | string | `kerf` |  |
| `database.sslmode` | string | `verify-full` |  |
| `database.sslrootcert` | string | `""` | sslrootcert path in the URL; default: the mounted caBundle (/etc/hub/db-ca/ca.pem) when set. |
| `database.params` | object | `{}` | Extra URL parameters, e.g. {pool_max_conns: "10", connect_timeout: "10"}. |
| `database.caBundle` | object | see the keys below | CA bundle for sslmode=verify-full (RDS: https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem), mounted at /etc/hub/db-ca/ca.pem. One of existingConfigMap, existingSecret or pem (inline, rendered into ConfigMap <release>-db-ca). |
| `database.caBundle.existingConfigMap` | string | `""` |  |
| `database.caBundle.existingSecret` | string | `""` |  |
| `database.caBundle.key` | string | `ca.pem` |  |
| `database.caBundle.pem` | string | `""` |  |
| `database.externalSecret` | object | see the keys below |  |
| `database.externalSecret.enabled` | boolean | `false` |  |
| `database.externalSecret.source` | string | `rds` | ds: remoteRef.key is an RDS-managed secret (JSON with username and password); the URL is<br>`postgres://<username>:<password>@host:port/name?sslmode=...&sslrootcert=...&params`<br>url: the property (or the whole secret) is the full URL. |
| `database.externalSecret.remoteRef` | object | see the keys below |  |
| `database.externalSecret.remoteRef.key` | string | `""` | e.g. rds!db-0123abcd-... or kerf/hub/database |
| `database.externalSecret.remoteRef.property` | string | `""` |  |
| `database.externalSecret.remoteRef.version` | string | `""` |  |
| `database.externalSecret.refreshInterval` | string | `""` | default externalSecrets.refreshInterval |
| `database.externalSecret.secretStoreRef` | object | `{}` | default externalSecrets.secretStoreRef |
| `externalSecrets` | object | see the keys below | External Secrets Operator settings shared by every externalSecret of this chart. |
| `externalSecrets.apiVersion` | string | `external-secrets.io/v1` |  |
| `externalSecrets.refreshInterval` | string | `1h` |  |
| `externalSecrets.secretStoreRef` | object | `{}` | e.g. {kind: ClusterSecretStore, name: aws-secrets-manager}; falls back to global.externalSecrets. |
| `devSeed` | object | see the keys below | Development seed: registers clusters and tokens; the tokens are stored in Secret hub-dev-tokens. |
| `devSeed.enabled` | boolean | `false` |  |
| `devSeed.clusters` | array | `[]` |  |
| `devSeed.tokens` | object | `{}` | key = cluster name (agent token for that cluster), "ui" or "admin". |
| `devSeed.users` | array | `[]` | Local users created when missing ({username, role, password, displayName}); stored in Secret hub-dev-users. Development only. |
| `auth` | object | see the keys below | Local users and browser sessions. |
| `auth.cookieSecure` | boolean | `true` | Secure attribute of the session cookie; false only for plain-HTTP development. |
| `auth.sessionTTL` | string | `12h` |  |
| `auth.maxFailedLogins` | integer | `5` |  |
| `auth.lockoutDuration` | string | `15m` |  |
| `auth.bootstrapAdmin` | object | see the keys below | The initial admin, created when the database has no users. The password comes from existingSecret/existingSecretKey, externalSecret (Secret <release>-bootstrap-admin) or password (development; rendered into Secret <release>-bootstrap-admin). With none of them, generate: true makes the chart generate one, once (Secret <release>-bootstrap-admin, kept across upgrades and uninstall; Helm lookup, so not with `helm template` or Argo CD), and the admin must change it at first sign-in. A password you set is not forced. generate: false and no password: no bootstrap admin. Not generated with devSeed. |
| `auth.bootstrapAdmin.username` | string | `admin` |  |
| `auth.bootstrapAdmin.generate` | boolean | `true` |  |
| `auth.bootstrapAdmin.password` | string | `""` |  |
| `auth.bootstrapAdmin.existingSecret` | string | `""` |  |
| `auth.bootstrapAdmin.existingSecretKey` | string | `password` |  |
| `auth.bootstrapAdmin.externalSecret` | object | see the keys below |  |
| `auth.bootstrapAdmin.externalSecret.enabled` | boolean | `false` |  |
| `auth.bootstrapAdmin.externalSecret.remoteRef` | object | see the keys below |  |
| `auth.bootstrapAdmin.externalSecret.remoteRef.key` | string | `""` | e.g. kerf/hub/bootstrap-admin |
| `auth.bootstrapAdmin.externalSecret.remoteRef.property` | string | `""` | e.g. password (JSON secret); empty = the whole secret string |
| `auth.bootstrapAdmin.externalSecret.remoteRef.version` | string | `""` |  |
| `auth.bootstrapAdmin.externalSecret.refreshInterval` | string | `""` |  |
| `auth.bootstrapAdmin.externalSecret.secretStoreRef` | object | `{}` |  |
| `auth.local` | object | see the keys below | Sign-in with a password. enabled: false keeps it only for the break-glass admin (breakGlassUser, default bootstrapAdmin.username), for when the identity provider is down; needs oidc.enabled. |
| `auth.local.enabled` | boolean | `true` |  |
| `auth.local.breakGlassUser` | string | `""` |  |
| `auth.oidc` | object | see the keys below | Sign-in with an OpenID Connect provider (Okta, Microsoft Entra ID, Google, Keycloak, Dex, ...): authorization code flow with PKCE. Settings per provider: docs/auth.md. |
| `auth.oidc.enabled` | boolean | `false` |  |
| `auth.oidc.name` | string | `""` | the sign-in button: "Sign in with <name>" (default SSO) |
| `auth.oidc.issuer` | string | `""` | e.g. https://example.okta.com |
| `auth.oidc.clientID` | string | `""` |  |
| `auth.oidc.redirectURL` | string | `""` | This UI's callback, registered with the provider: https://<UI host>/v1/auth/oidc/callback |
| `auth.oidc.scopes` | array | `[]` | Default: openid, profile, email, offline_access (the refresh token lets the hub re-read the groups). Dex, Keycloak: add groups. |
| `auth.oidc.authParams` | object | `{}` | extra authorization parameters, e.g. Google: {access_type: offline, prompt: consent} |
| `auth.oidc.groupsClaim` | string | `groups` |  |
| `auth.oidc.usernameClaim` | string | `email` | users are named after this claim (lowercased) |
| `auth.oidc.roleMappings` | array | `[]` | Groups to roles; the highest matching role wins. e.g. - {group: platform-admins, role: admin} - {group: platform, role: operator} - {group: engineering, role: viewer} |
| `auth.oidc.defaultRole` | string | `""` | ole when no group matches; "" denies the sign-in |
| `auth.oidc.refreshInterval` | string | `5m` | how often a session re-reads the groups (a removed group ends the role) |
| `auth.oidc.connectAddress` | string | `""` | host:port the hub connects to for the issuer's host, when it reaches the provider by another address than browsers (split-horizon networks). URLs and TLS names stay the issuer's. Empty: the issuer's. |
| `auth.oidc.clientSecret` | object | see the keys below | The client secret: existingSecret/existingSecretKey, externalSecret (Secret <release>-oidc), or value (development only; rendered into Secret <release>-oidc). |
| `auth.oidc.clientSecret.existingSecret` | string | `""` |  |
| `auth.oidc.clientSecret.existingSecretKey` | string | `client-secret` |  |
| `auth.oidc.clientSecret.value` | string | `""` |  |
| `auth.oidc.clientSecret.externalSecret` | object | see the keys below |  |
| `auth.oidc.clientSecret.externalSecret.enabled` | boolean | `false` |  |
| `auth.oidc.clientSecret.externalSecret.remoteRef` | object | see the keys below |  |
| `auth.oidc.clientSecret.externalSecret.remoteRef.key` | string | `""` | e.g. kerf/hub/oidc |
| `auth.oidc.clientSecret.externalSecret.remoteRef.property` | string | `""` | e.g. clientSecret (JSON secret); empty = the whole secret string |
| `auth.oidc.clientSecret.externalSecret.remoteRef.version` | string | `""` |  |
| `auth.oidc.clientSecret.externalSecret.refreshInterval` | string | `""` |  |
| `auth.oidc.clientSecret.externalSecret.secretStoreRef` | object | `{}` |  |
| `localTokens` | object | see the keys below | Tokens of this release's own services, in the umbrella chart (global.mode hub and all): the chart generates them once (Secret <release>-local-tokens, keys pricing and, in mode all, agent; kept across upgrades and uninstall) and the hub stores their hashes at start-up, so pricing and the local agent connect without a manual token. Pricing and the agent use them unless their hub.tokenSecret names another source. Helm lookup keeps them stable: with `helm template` or Argo CD every render makes new ones (the hub restarts and accepts them; the old ones stay valid until revoked), so there give pricing and the agent their own Secrets instead. |
| `localTokens.enabled` | boolean | `true` |  |
| `logLevel` | string | `info` |  |
| `config` | object | see the keys below | Hub configuration file (internal/hub/config.go). databaseURL and devSeed are injected by the chart. |
| `config.listen` | string | `:8080` |  |
| `config.prometheusURL` | string | `http://victoria-metrics.monitoring.svc:8428` |  |
| `config.prometheusTimeout` | string | `2m` |  |
| `config.corsOrigins` | array | `[]` |  |
| `config.agentStaleAfter` | string | `10m` |  |
| `config.recommender` | object | see the keys below |  |
| `config.recommender.interval` | string | `24h` |  |
| `config.history` | object | see the keys below |  |
| `config.history.fullResolution` | string | `168h` |  |
| `config.history.retention` | string | `2160h` |  |
| `config.teams` | object | see the keys below | Team ownership (docs/teams.md). Teams are defined in Admin -> Teams; a workload belongs to the team its owner label names (the workload's own label wins over its namespace's), else to the first team whose namespace selector matches, else to Unassigned. |
| `config.teams.ownerLabel` | string | `team` | label key on workloads and namespaces whose value is a team id |
| `config.onboarding` | object | see the keys below | Cluster onboarding and readiness (internal/hub/config.go OnboardingConfig). |
| `config.onboarding.docsBaseURL` | string | `""` | Readiness docs links: <docsBaseURL>/readiness#<check id>. |
| `config.onboarding.checkInterval` | string | `1h` |  |
| `config.onboarding.agentPullInterval` | string | `1m` |  |
| `config.onboarding.minAgentVersion` | string | `""` |  |
| `config.onboarding.hubURL` | string | `""` | Install snippets of the cluster wizard: the hub URL as agents reach it and the umbrella chart kerf, installed with global.mode spoke (oci://registry/path/kerf, <repo URL>\|kerf, or a local chart path). |
| `config.onboarding.chart` | string | `""` |  |
| `config.onboarding.chartVersion` | string | `""` | Empty: the hub's version when it is a release version (the agent matches the hub). |
| `config.onboarding.imageRegistry` | string | `""` | global.imageRegistry of the snippets. Empty: the host of an oci:// chart. |
| `updateCheck` | object | see the keys below | Check for a newer stable Kerf release (Admin -> Maintenance). The leader reads the public GitHub releases of kerf-dev/kerf once per interval: a plain unauthenticated GET with User-Agent kerf-hub/<version>; nothing about this installation is sent. Set enabled: false for air-gapped sites (or no egress to api.github.com). Behind a proxy, set HTTPS_PROXY / NO_PROXY in extraEnv. The result is shown on the Maintenance page and exported as kerf_update_available{current,latest}. |
| `updateCheck.enabled` | boolean | `true` |  |
| `updateCheck.interval` | string | `24h` | Go duration, at least 10m; a random delay of up to a tenth of it (at most 1h) is added. |
| `updateCheck.url` | string | `https://api.github.com/repos/kerf-dev/kerf/releases` | The releases API (a mirror or a test server). |
| `notifications` | object | see the keys below | Notifications (Admin -> Notifications, docs/notifications.md): Slack, Microsoft Teams and signed webhooks, rules, the weekly digest. Rules and the digest are set in the UI; channels can come from here too (read-only in the UI). A channel's URL (a credential) and a webhook channel's signing secret are read from a Secret you create or sync (External Secrets); the hub encrypts what the UI stores with the datasource key below. The hub needs egress to the destinations. |
| `notifications.baseURL` | string | `""` | Where the web UI is reachable: messages link to <baseURL>/workloads/... Empty: no links. |
| `notifications.rateLimitPerMinute` | integer | `20` | Messages per minute and channel; the surplus waits. |
| `notifications.allowInsecureHTTP` | boolean | `false` | Accept http:// channel URLs (a receiver inside the cluster, tests). Default: HTTPS only. |
| `notifications.channels` | array | `[]` | `channels:`<br>- name: platform-alerts         # a DNS label<br>`type: slack                   # slack \| teams \| webhook`<br>`existingSecret: kerf-slack    # Secret in this namespace`<br>`urlKey: url                   # key holding the webhook URL (default url)`<br>`secretKey: ""                 # webhook only: key holding the HMAC signing secret (optional)`<br>`enabled: true` |
| `onboarding` | object | see the keys below | Datasource secrets (bearer tokens, passwords, mTLS keys) are encrypted at rest with this key: create: the chart generates a random key once and keeps it (helm.sh/resource-policy: keep); back the Secret up. Or point existingSecret at your own Secret (32 bytes, base64 or hex, one key per line; the first encrypts, the others still decrypt during a rotation), or let externalSecret build Secret <release>-datasource-key from Secrets Manager (wins over create). |
| `onboarding.secretsKey` | object | see the keys below |  |
| `onboarding.secretsKey.create` | boolean | `true` |  |
| `onboarding.secretsKey.existingSecret` | string | `""` |  |
| `onboarding.secretsKey.existingSecretKey` | string | `key` |  |
| `onboarding.secretsKey.externalSecret` | object | see the keys below |  |
| `onboarding.secretsKey.externalSecret.enabled` | boolean | `false` |  |
| `onboarding.secretsKey.externalSecret.remoteRef` | object | see the keys below |  |
| `onboarding.secretsKey.externalSecret.remoteRef.key` | string | `""` | e.g. kerf/hub/datasource-key |
| `onboarding.secretsKey.externalSecret.remoteRef.property` | string | `""` |  |
| `onboarding.secretsKey.externalSecret.remoteRef.version` | string | `""` |  |
| `onboarding.secretsKey.externalSecret.refreshInterval` | string | `""` |  |
| `onboarding.secretsKey.externalSecret.secretStoreRef` | object | `{}` |  |
| `resources` | object | see the keys below |  |
| `resources.requests` | object | see the keys below |  |
| `resources.requests.cpu` | string | `50m` |  |
| `resources.requests.memory` | string | `256Mi` |  |
| `resources.limits` | object | see the keys below |  |
| `resources.limits.memory` | string | `1Gi` |  |
| `podSecurityContext` | object | see the keys below |  |
| `podSecurityContext.runAsNonRoot` | boolean | `true` |  |
| `podSecurityContext.seccompProfile` | object | see the keys below |  |
| `podSecurityContext.seccompProfile.type` | string | `RuntimeDefault` |  |
| `securityContext` | object | see the keys below |  |
| `securityContext.allowPrivilegeEscalation` | boolean | `false` |  |
| `securityContext.readOnlyRootFilesystem` | boolean | `true` |  |
| `securityContext.capabilities` | object | see the keys below |  |
| `securityContext.capabilities.drop` | array | `["ALL"]` |  |
| `startupProbe` | object | see the keys below | The hub applies the database migrations before it listens: the startup probe gives them periodSeconds x failureThreshold (default 10 minutes) before liveness takes over. |
| `startupProbe.enabled` | boolean | `true` |  |
| `startupProbe.periodSeconds` | integer | `5` |  |
| `startupProbe.failureThreshold` | integer | `120` |  |
| `networkPolicy` | object | see the keys below | NetworkPolicy (needs a CNI that enforces it; on EKS the VPC CNI network policy agent). Ingress to the API port: web and pricing pods of this namespace, plus `ingress.from` (NetworkPolicyPeers): the ingress controller or the ALB (target-type ip: the VPC CIDRs as ipBlock), agents in other clusters (their NAT CIDRs) and `ingress.metricsFrom` (the scraper). Egress: DNS, the database port, TCP `egress.ports` to any destination (datasources) and the raw rules in `egress.to`. |
| `networkPolicy.enabled` | boolean | `false` |  |
| `networkPolicy.ingress` | object | see the keys below |  |
| `networkPolicy.ingress.from` | array | `[]` |  |
| `networkPolicy.ingress.metricsFrom` | array | `[]` |  |
| `networkPolicy.egress` | object | see the keys below |  |
| `networkPolicy.egress.enabled` | boolean | `true` |  |
| `networkPolicy.egress.ports` | array | `[443, 8428, 9090]` | Datasources (VictoriaMetrics 8428, Prometheus 9090, HTTPS 443). The database port is added. |
| `networkPolicy.egress.to` | array | `[]` |  |
| `serviceMonitor` | object | see the keys below | Prometheus Operator ServiceMonitor or VictoriaMetrics VMServiceScrape for /metrics. Keep honorLabels: the hub sets `cluster` on its series itself (docs/contracts/metrics.md). |
| `serviceMonitor.enabled` | boolean | `false` |  |
| `serviceMonitor.kind` | string | `ServiceMonitor` | or VMServiceScrape |
| `serviceMonitor.namespace` | string | `""` |  |
| `serviceMonitor.interval` | string | `30s` |  |
| `serviceMonitor.scrapeTimeout` | string | `""` |  |
| `serviceMonitor.honorLabels` | boolean | `true` |  |
| `serviceMonitor.labels` | object | `{}` | e.g. release: kube-prometheus-stack |
| `serviceMonitor.relabelings` | array | `[]` |  |
| `serviceMonitor.metricRelabelings` | array | `[]` |  |
| `prometheusRule` | object | see the keys below | Alerting rules (PrometheusRule or VMRule). They need the fleet's series (every spoke's agent and webhook metrics), so evaluate them where those land: the hub's VictoriaMetrics (vmalert, VMRule) or a Prometheus that reads it. |
| `prometheusRule.enabled` | boolean | `false` |  |
| `prometheusRule.kind` | string | `PrometheusRule` | or VMRule |
| `prometheusRule.namespace` | string | `""` |  |
| `prometheusRule.labels` | object | `{}` |  |
| `prometheusRule.additionalLabels` | object | `{}` | Extra labels on every alert (e.g. team: platform). |
| `prometheusRule.alerts` | object | see the keys below |  |
| `prometheusRule.alerts.agentOffline` | object | see the keys below | The hub has not heard from a cluster's agent (readiness check agent.connected). |
| `prometheusRule.alerts.agentOffline.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.agentOffline.for` | string | `10m` |  |
| `prometheusRule.alerts.agentOffline.severity` | string | `warning` |  |
| `prometheusRule.alerts.readinessBlocking` | object | see the keys below | A blocking readiness check is failing (the cluster cannot reach Automation ready). |
| `prometheusRule.alerts.readinessBlocking.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.readinessBlocking.for` | string | `30m` |  |
| `prometheusRule.alerts.readinessBlocking.severity` | string | `warning` |  |
| `prometheusRule.alerts.recommenderFailing` | object | see the keys below | Recommender runs failed (status failed or partial) within 2 recommender intervals. |
| `prometheusRule.alerts.recommenderFailing.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.recommenderFailing.for` | string | `0m` |  |
| `prometheusRule.alerts.recommenderFailing.severity` | string | `warning` |  |
| `prometheusRule.alerts.recommenderStale` | object | see the keys below | No successful recommender run for a cluster for 2 intervals. staleAfter: a Go duration; empty = 2 x config.recommender.interval (an interval set in the admin UI is not seen here). |
| `prometheusRule.alerts.recommenderStale.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.recommenderStale.for` | string | `15m` |  |
| `prometheusRule.alerts.recommenderStale.severity` | string | `warning` |  |
| `prometheusRule.alerts.recommenderStale.staleAfter` | string | `""` |  |
| `prometheusRule.alerts.noLeader` | object | see the keys below | No hub replica holds the leader lock (kerf_hub_leader). |
| `prometheusRule.alerts.noLeader.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.noLeader.for` | string | `5m` |  |
| `prometheusRule.alerts.noLeader.severity` | string | `critical` |  |
| `prometheusRule.alerts.webhookErrors` | object | see the keys below | Webhook admissions ending in error, as a share of all admissions of a cluster. |
| `prometheusRule.alerts.webhookErrors.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.webhookErrors.for` | string | `15m` |  |
| `prometheusRule.alerts.webhookErrors.severity` | string | `warning` |  |
| `prometheusRule.alerts.webhookErrors.ratio` | number | `0.05` |  |
| `prometheusRule.alerts.pricingStale` | object | see the keys below | The pricing service has not refreshed prices for staleAfter (cost series go stale). |
| `prometheusRule.alerts.pricingStale.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.pricingStale.for` | string | `5m` |  |
| `prometheusRule.alerts.pricingStale.severity` | string | `warning` |  |
| `prometheusRule.alerts.pricingStale.staleAfter` | string | `30m` |  |
| `prometheusRule.alerts.updateAvailable` | object | see the keys below | A newer stable Kerf release exists (kerf_update_available, needs updateCheck.enabled). |
| `prometheusRule.alerts.updateAvailable.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.updateAvailable.for` | string | `1h` |  |
| `prometheusRule.alerts.updateAvailable.severity` | string | `info` |  |
| `prometheusRule.alerts.notificationFailing` | object | see the keys below | Notifications failed for good (kerf_notification_deliveries_total{result="failed"}). |
| `prometheusRule.alerts.notificationFailing.enabled` | boolean | `true` |  |
| `prometheusRule.alerts.notificationFailing.for` | string | `0m` |  |
| `prometheusRule.alerts.notificationFailing.severity` | string | `warning` |  |
| `prometheusRule.additionalRules` | array | `[]` |  |
| `dashboards` | object | see the keys below | Grafana dashboards (dashboards/*.json) as ConfigMaps for the Grafana sidecar. They query the datasource uid `victoriametrics` by default (dashboard variable `datasource`). |
| `dashboards.enabled` | boolean | `false` |  |
| `dashboards.namespace` | string | `""` | default: the release namespace |
| `dashboards.labels` | object | see the keys below |  |
| `dashboards.labels.grafana_dashboard` | string | `1` |  |
| `dashboards.annotations` | object | see the keys below |  |
| `dashboards.annotations.grafana_folder` | string | `kerf` |  |
