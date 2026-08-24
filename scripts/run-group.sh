#!/usr/bin/env bash
# Runs one catalog group, so each invocation finishes well inside a single call's budget.
set -uo pipefail
# Deliberately not ${KUBECONFIG:-...}. Shells here export KUBECONFIG pointing at production,
# and this script creates and deletes real collections and data objects -- inheriting an
# ambient value would aim that wherever the last thing to touch the variable was pointing.
# Override with SHADOW_KUBECONFIG, which has to be set on purpose.
export KUBECONFIG=${SHADOW_KUBECONFIG:-$HOME/.kube/qa.conf}

# And refuse a production cluster whatever the variable says. This harness writes; there is
# no version of it that should run against prod.
context=$(kubectl config current-context 2>/dev/null || echo unknown)
case "$context" in
    *prod*|*production*)
        echo "refusing to run against context $context: this harness creates and deletes data" >&2
        exit 1
        ;;
esac
cd "$(dirname "$0")/.."
SC=$(mktemp -d)
GROUP=$1; RUN_ID=$2; U=${SHADOW_USER:-wregglej}

DIR=$SC/catalog-$GROUP
rm -rf "$DIR"; mkdir -p "$DIR"
cp "test/shadow/catalog/$GROUP.yaml" "$DIR/"

POD=$(kubectl -n qa get pods -l de-app=data-info-next -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -z "$POD" ]; then
    echo "no data-info-next pod in namespace qa on context $context" >&2
    exit 1
fi
kubectl -n qa port-forward svc/data-info 18080:80   >/dev/null 2>&1 & PF1=$!
kubectl -n qa port-forward "pod/$POD" 18081:60000   >/dev/null 2>&1 & PF2=$!
kubectl -n qa port-forward svc/async-tasks 18082:80 >/dev/null 2>&1 & PF3=$!
trap 'kill $PF1 $PF2 $PF3 2>/dev/null; rm -rf "$SC"' EXIT
sleep 4

go run ./cmd/dishadow \
  -reference http://127.0.0.1:18080 \
  -candidate http://127.0.0.1:18081 \
  -async-tasks http://127.0.0.1:18082 \
  -catalog "$DIR" \
  -user "$U" -zone cyverse \
  -root "/cyverse/home/$U/shadow-scratch/fixture" \
  -scratch "/cyverse/home/$U/shadow-scratch/runs" \
  -run-id "$RUN_ID"
