# -*- mode: Python -*-
# Inner loop for Kerf on the local k3d fleet (D-010). Run `make dev-up` first.
#
#   tilt up                  everything below that exists
#   tilt up agent webhook    only those resources
#
# Tilt drives one kube context natively, but our components span three clusters, so every component is
# a local_resource that builds its image, pushes it to the k3d registry and helm-installs its chart into
# the right cluster(s) via dev/scripts/tilt-deploy.sh. A component is skipped until its chart exists
# in deploy/charts/<name>/ (see the conventions at the top of tilt-deploy.sh).

COMPONENTS = [
    # (name, clusters it runs in, extra source dirs besides cmd/<name> and internal/<name>)
    ('hub', ['ro-hub'], ['internal/recommender', 'db/migrations']),
    ('pricing', ['ro-hub'], []),
    ('agent', ['ro-spoke-1', 'ro-spoke-2'], []),
    ('webhook', ['ro-spoke-1', 'ro-spoke-2'], []),
    ('web', ['ro-hub'], ['web/src', 'web/nginx', 'web/Dockerfile', 'web/package.json']),
]

# Tilt gives local_resource commands a kubeconfig holding only its own context; ours need all three
# k3d contexts, so pass the user's kubeconfig through.
KUBE_ENV = {'KUBECONFIG': os.getenv('KUBECONFIG', '') or os.path.join(os.getenv('HOME'), '.kube', 'config')}

def existing(paths):
    return [p for p in paths if os.path.exists(p)]

for name, clusters, extra in COMPONENTS:
    chart = 'deploy/charts/%s' % name
    if not os.path.exists(chart):
        print('tilt: skipping %s (%s does not exist yet)' % (name, chart))
        continue
    local_resource(
        name,
        cmd='dev/scripts/tilt-deploy.sh %s %s' % (name, ' '.join(clusters)),
        deps=existing([chart, 'cmd/' + name, 'internal/' + name, 'api', 'go.mod', 'go.sum',
                       'build/' + name, 'Dockerfile'] + extra),
        ignore=['**/*_test.go', 'web/node_modules', 'web/dist'],
        labels=['components'],
        allow_parallel=True,
        env=KUBE_ENV,
    )

# Platform helpers: always available, cheap, idempotent.
local_resource(
    'dashboards',
    cmd='dev/scripts/sync-dashboards.sh',
    deps=existing(['dashboards', 'dev/scripts/sync-dashboards.sh']),
    labels=['dev'],
    allow_parallel=True,
    env=KUBE_ENV,
)

local_resource(
    'workloads',
    cmd='dev/scripts/sync-workloads.sh',
    deps=['dev/workloads', 'dev/scripts/sync-workloads.sh'],
    labels=['dev'],
    allow_parallel=True,
    env=KUBE_ENV,
)
