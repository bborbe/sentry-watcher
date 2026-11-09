#!/usr/bin/env bash
set -euo pipefail
# One-time bootstrap for the cluster build arm. NOT invoked by `make build`.
# Creates the build namespace and documents creating the push secret from
# dockerconfig.json.tpl rendered with TeamVault values.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/cluster-alias.sh
source "$SCRIPT_DIR/cluster-alias.sh"

BUILD_TARGET="${BUILD_TARGET:-sun}"
NAMESPACE="${NAMESPACE:-build}"
SECRET="${SECRET:-buildkit-push}"
TEMPLATE="${TEMPLATE:-$SCRIPT_DIR/dockerconfig.json.tpl}"

KUBECONFIG_PATH="$(cluster_kubeconfig "$BUILD_TARGET")"
KUBECTL=(kubectl --kubeconfig "$KUBECONFIG_PATH")

"${KUBECTL[@]}" create namespace "$NAMESPACE" --dry-run=client -o yaml | "${KUBECTL[@]}" apply -f -

cat >&2 <<EOF
Bootstrap namespace '$NAMESPACE'.
Next: render '$TEMPLATE' with TeamVault values, then create the secret:
  kubectl --kubeconfig "$KUBECONFIG_PATH" -n "$NAMESPACE" create secret generic "$SECRET" \\
    --from-file=.dockerconfigjson=<rendered-file> --type=kubernetes.io/dockerconfigjson
EOF
