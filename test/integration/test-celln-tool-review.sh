#!/usr/bin/env bash
# Actual API + trusted Celln CLI review proof. Fixture is deliberately not
# executable: this tests metadata publication, never guest conformance/readiness.
set -euo pipefail
: "${CELLN_CATALOGUE_KUBECONFIG:?explicit isolated kubeconfig required}"
: "${CELLN_REVIEW_BINARY:?absolute trusted Celln binary required}"
: "${SYMPOZIUM_REVIEW_BINARY:?absolute built Sympozium CLI required}"
: "${CELLN_REVIEW_FIXTURE:?public prepare_review_fixture directory required}"
k() { kubectl --kubeconfig "$CELLN_CATALOGUE_KUBECONFIG" "$@"; }
[[ $(k config current-context) == kind-celln-deployed ]] || { echo 'Refusing non-test context' >&2; exit 1; }
namespace="celln-review-proof-$$"
k create namespace "$namespace"
trap 'k delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null' EXIT
symp() { "$SYMPOZIUM_REVIEW_BINARY" --kubeconfig "$CELLN_CATALOGUE_KUBECONFIG" -n "$namespace" celln-tool "$@"; }
approve() {
  symp approve "$1" --reviewed-uid "$2" --reviewed-spec-sha256 "$3" \
    --celln-binary "$CELLN_REVIEW_BINARY" --policy-root "$CELLN_REVIEW_FIXTURE" --bundle-dir "$CELLN_REVIEW_FIXTURE"
}
k create -n "$namespace" -f "$CELLN_REVIEW_FIXTURE/submission.json"
inspection=$(symp inspect review-fixture)
uid=$(jq -r '.identity.uid' <<<"$inspection")
spec_hash=$(jq -r '.identity.specSHA256' <<<"$inspection")
if approve review-fixture stale-uid "$spec_hash"; then echo 'Accepted stale review' >&2; exit 1; fi
if k get cellntool review-fixture -n "$namespace" >/dev/null 2>&1; then echo 'Published after refusal' >&2; exit 1; fi
approve review-fixture "$uid" "$spec_hash"
k get cellntool review-fixture -n "$namespace" -o json |
  jq -e --arg uid "$uid" --arg hash "$spec_hash" \
    '.metadata.annotations["celln.sympozium.ai/reviewed-submission-uid"] == $uid and .metadata.annotations["celln.sympozium.ai/reviewed-spec-sha256"] == $hash and ((.status.conditions // []) | length) == 0' >/dev/null
if approve review-fixture "$uid" "$spec_hash"; then echo 'Overwrote existing revision' >&2; exit 1; fi
jq '.metadata.name="wrong-schema" | .spec.argumentsSchema=.spec.resultSchema' "$CELLN_REVIEW_FIXTURE/submission.json" | k create -n "$namespace" -f -
inspection=$(symp inspect wrong-schema)
if approve wrong-schema "$(jq -r '.identity.uid' <<<"$inspection")" "$(jq -r '.identity.specSHA256' <<<"$inspection")"; then echo 'Accepted wrong schema bytes' >&2; exit 1; fi
if k get cellntool wrong-schema -n "$namespace" >/dev/null 2>&1; then echo 'Published after schema refusal' >&2; exit 1; fi
echo 'PASS actual API/CLI reviewed publication, stale review refusal, immutable create and exact schema bytes'
echo 'No Ready status, runtime/tool execution or provider call; temporary namespace removed by cleanup'
