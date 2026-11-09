#!/usr/bin/env bash
# Maps cluster aliases to kubeconfig files. Sourceable; defines cluster_kubeconfig.
# An alias is accepted when $KUBE_DIR (default $HOME/.kube) contains a file of
# that name, so `sun` maps to $HOME/.kube/sun.
cluster_kubeconfig() {
	local name="${1:-}"
	local dir="${KUBE_DIR:-$HOME/.kube}"
	if [ -z "$name" ]; then
		echo "ERROR: BUILD_TARGET (cluster alias) is required; accepted: $(ls "$dir" 2>/dev/null | tr '\n' ' ')" >&2
		return 1
	fi
	local cfg="$dir/$name"
	if [ ! -f "$cfg" ]; then
		echo "ERROR: unknown cluster '$name'; accepted: $(ls "$dir" 2>/dev/null | tr '\n' ' ')" >&2
		return 1
	fi
	printf '%s\n' "$cfg"
}
