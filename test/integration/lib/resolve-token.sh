#!/usr/bin/env bash
# Shared helper for resolving the apiserver bearer token from a live
# cluster. Sourced by every test/integration/test-*.sh script that needs
# to authenticate against sympozium-apiserver.
#
# Discovery order:
#   1. APISERVER_TOKEN env var, if non-empty (manual override for CI).
#   2. Literal env value on the apiserver deployment
#      (spec.apiserver.webUI.token == pinned literal).
#   3. Volume-mounted Secret — the production chart path.
#   4. Legacy env.valueFrom.secretKeyRef — for not-yet-upgraded deployments.
#   5. Auth disabled (APISERVER_TOKEN="").
#
# Sets the global APISERVER_TOKEN. Requires these env vars to be set by the
# caller:
#   APISERVER_NAMESPACE — namespace of the sympozium-apiserver deployment.
#
# Also exports SECRET_NAME so callers can rotate the Secret via kubectl
# patch without re-resolving the name.

# Avoid double-sourcing.
if [[ -n "${VELATIR_RESOLVE_TOKEN_LOADED:-}" ]]; then
  return 0
fi
VELATIR_RESOLVE_TOKEN_LOADED=1

resolve_apiserver_token() {
  if [[ -n "${APISERVER_TOKEN}" ]]; then
    return 0
  fi

  local token

  # 1. Literal env value — set when apiserver.webUI.token is pinned in values.
  token="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].value}' 2>/dev/null || true)"
  if [[ -n "$token" ]]; then
    APISERVER_TOKEN="$token"
    return 0
  fi

  # 2. Volume-mounted Secret — production chart with no webUI.token.
  #    The apiserver hot-reloads the token by re-reading this file on every
  #    request, so a Secret rotation propagates without a pod restart.
  local secret_name
  secret_name="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.volumes[?(@.name=="sympozium-ui-token")].secret.secretName}' 2>/dev/null || true)"
  if [[ -n "$secret_name" ]]; then
    token="$(kubectl get secret -n "${APISERVER_NAMESPACE}" "$secret_name" \
      -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null || true)"
    if [[ -n "$token" ]]; then
      APISERVER_TOKEN="$token"
      export SECRET_NAME="$secret_name"
      return 0
    fi
  fi

  # 3. Legacy chart (env.valueFrom.secretKeyRef) — kept for deployments
  #    that have not yet been upgraded to the volume mount.
  local legacy_secret_name legacy_secret_key
  legacy_secret_name="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.name}' 2>/dev/null || true)"
  legacy_secret_key="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="SYMPOZIUM_UI_TOKEN")].valueFrom.secretKeyRef.key}' 2>/dev/null || true)"
  if [[ -z "$legacy_secret_key" ]]; then legacy_secret_key="token"; fi
  if [[ -n "$legacy_secret_name" ]]; then
    token="$(kubectl get secret -n "${APISERVER_NAMESPACE}" "$legacy_secret_name" \
      -o jsonpath="{.data.${legacy_secret_key}}" 2>/dev/null | base64 -d 2>/dev/null || true)"
    if [[ -n "$token" ]]; then
      APISERVER_TOKEN="$token"
      export SECRET_NAME="$legacy_secret_name"
      return 0
    fi
  fi

  # Token may be disabled in some local setups.
  APISERVER_TOKEN=""
}