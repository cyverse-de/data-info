#!/usr/bin/env bash
# Runs one catalog group, so each invocation finishes well inside a single call's budget.
set -uo pipefail
export KUBECONFIG=${KUBECONFIG:-~/.kube/qa.conf}
cd "$(dirname "$0")/.."
SC=$(mktemp -d)
GROUP=$1; RUN_ID=$2; U=${SHADOW_USER:-wregglej}

DIR=$SC/catalog-$GROUP
rm -rf "$DIR"; mkdir -p "$DIR"
cp "test/shadow/catalog/$GROUP.yaml" "$DIR/"

POD=$(kubectl -n qa get pods -l de-app=data-info-next -o jsonpath='{.items[0].metadata.name}')
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
