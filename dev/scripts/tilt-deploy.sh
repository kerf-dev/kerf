#!/usr/bin/env bash
# Build, push and helm-deploy one component to one or more k3d clusters. Called by the Tiltfile.
# Usage: tilt-deploy.sh <component> <cluster>...
#
# Conventions for component owners (WO-03/04/06/07):
#   chart       deploy/charts/<component>/ (required; the component is skipped without it)
#   values      deploy/charts/<component>/values-dev.yaml and values-<cluster>.yaml, if present
#   image       first existing of cmd/<component>/Dockerfile, build/<component>/Dockerfile,
#               build/<component>.Dockerfile, or a root Dockerfile taking --build-arg COMPONENT=<component>;
#               build context is the repo root. Without one the chart's default image is used.
#   overrides   image.repository, image.tag, and clusterName=<cluster> are set on every install
#   namespace   $RO_NAMESPACE (default resource-optimizer)
. "$(dirname "$0")/lib.sh"

name="$1"; shift
export LOG_PREFIX="tilt:$name"
chart="$ROOT/deploy/charts/$name"
ns="${RO_NAMESPACE:-resource-optimizer}"
[[ -d "$chart" ]] || { log "deploy/charts/$name does not exist yet, skipping"; exit 0; }

dockerfile="" build_args=""
for cand in "$ROOT/cmd/$name/Dockerfile" "$ROOT/build/$name/Dockerfile" "$ROOT/build/$name.Dockerfile"; do
  if [[ -f "$cand" ]]; then dockerfile="$cand"; break; fi
done
if [[ -z "$dockerfile" && -f "$ROOT/Dockerfile" ]]; then
  dockerfile="$ROOT/Dockerfile"; build_args="--build-arg COMPONENT=$name"
fi

set_args=()
if [[ -n "$dockerfile" ]]; then
  tag="dev-$(date +%Y%m%d-%H%M%S)"
  image="resource-optimizer/$name"
  log "building $image:$tag from ${dockerfile#"$ROOT/"}"
  # shellcheck disable=SC2086
  docker build -f "$dockerfile" $build_args -t "localhost:$REGISTRY_PORT/$image:$tag" "$ROOT"
  docker push "localhost:$REGISTRY_PORT/$image:$tag" >/dev/null
  set_args=(--set-string "image.repository=k3d-$REGISTRY:$REGISTRY_PORT/$image" --set-string "image.tag=$tag")
else
  warn "no Dockerfile for $name; deploying the chart's default image"
fi

helm dependency build "$chart" >/dev/null 2>&1 || true
for c in "$@"; do
  values=()
  for f in "$chart/values-dev.yaml" "$chart/values-$c.yaml"; do
    if [[ -f "$f" ]]; then values+=(-f "$f"); fi
  done
  log "helm upgrade --install $name -> $(ctx "$c")/$ns"
  helm upgrade --install "$name" "$chart" --kube-context "$(ctx "$c")" -n "$ns" --create-namespace \
    --wait --timeout 5m ${values[@]+"${values[@]}"} ${set_args[@]+"${set_args[@]}"} \
    --set-string "clusterName=$c"
done
