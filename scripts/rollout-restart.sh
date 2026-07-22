#!/usr/bin/env bash
# Rolling-restart the ai-agent Deployment via `kubectl rollout restart`.
#
# Usage:
#   ./scripts/rollout-restart.sh -n <namespace> [-d <deployment>] [-t <timeout>]
#
# Examples:
#   ./scripts/rollout-restart.sh -n prod
#   ./scripts/rollout-restart.sh -n staging -d ai-agent -t 180s
#
# Server is stateless (PROJECT.md §8.1) so a rolling restart with
# maxUnavailable: 0 (see deployments/k8s/deployment.yaml) is safe:
# kubectl replaces pods one at a time and only routes traffic to a new
# pod once its readinessProbe passes, so in-flight requests are never
# dropped onto a dead pod.

set -euo pipefail

DEPLOYMENT="ai-agent"
NAMESPACE=""
TIMEOUT="120s"
CONTEXT=""

usage() {
  cat <<EOF
Usage: $0 -n <namespace> [-d <deployment>] [-t <timeout>] [-c <kube-context>]

  -n  kubernetes namespace (required)
  -d  deployment name (default: ${DEPLOYMENT})
  -t  rollout status wait timeout (default: ${TIMEOUT}, e.g. 180s)
  -c  kubectl context to use (default: current context)
  -h  show this help
EOF
  exit 1
}

while getopts "n:d:t:c:h" opt; do
  case "$opt" in
    n) NAMESPACE="$OPTARG" ;;
    d) DEPLOYMENT="$OPTARG" ;;
    t) TIMEOUT="$OPTARG" ;;
    c) CONTEXT="$OPTARG" ;;
    h) usage ;;
    *) usage ;;
  esac
done

if [[ -z "$NAMESPACE" ]]; then
  echo "error: -n <namespace> is required" >&2
  usage
fi

if ! command -v kubectl >/dev/null 2>&1; then
  echo "error: kubectl not found in PATH" >&2
  exit 1
fi

KUBECTL=(kubectl)
if [[ -n "$CONTEXT" ]]; then
  KUBECTL+=(--context "$CONTEXT")
fi
KUBECTL+=(-n "$NAMESPACE")

if ! "${KUBECTL[@]}" get deployment "$DEPLOYMENT" >/dev/null 2>&1; then
  echo "error: deployment '$DEPLOYMENT' not found in namespace '$NAMESPACE'" >&2
  exit 1
fi

echo ">> rollout restart deployment/$DEPLOYMENT (namespace: $NAMESPACE)"
"${KUBECTL[@]}" rollout restart "deployment/$DEPLOYMENT"

echo ">> waiting for rollout to complete (timeout: $TIMEOUT)"
if ! "${KUBECTL[@]}" rollout status "deployment/$DEPLOYMENT" --timeout="$TIMEOUT"; then
  echo "error: rollout did not complete within $TIMEOUT — check pod status:" >&2
  "${KUBECTL[@]}" get pods -l "app=$DEPLOYMENT" -o wide
  exit 1
fi

echo ">> rollout complete"
"${KUBECTL[@]}" get pods -l "app=$DEPLOYMENT" -o wide
