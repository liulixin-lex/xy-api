#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'stable reuse handoff verification failed: %s\n' "$1" >&2
  exit 1
}

verification_file=''
cosign_file=''
source_sha=''
run_id=''
run_attempt=''
workflow_ref=''
workflow_sha=''
repository=''
digest=''
platform=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --verification)
      [ "$#" -ge 2 ] || fail '--verification requires a value'
      verification_file=$2
      shift 2
      ;;
    --cosign)
      [ "$#" -ge 2 ] || fail '--cosign requires a value'
      cosign_file=$2
      shift 2
      ;;
    --source-sha)
      [ "$#" -ge 2 ] || fail '--source-sha requires a value'
      source_sha=$2
      shift 2
      ;;
    --run-id)
      [ "$#" -ge 2 ] || fail '--run-id requires a value'
      run_id=$2
      shift 2
      ;;
    --run-attempt)
      [ "$#" -ge 2 ] || fail '--run-attempt requires a value'
      run_attempt=$2
      shift 2
      ;;
    --workflow-ref)
      [ "$#" -ge 2 ] || fail '--workflow-ref requires a value'
      workflow_ref=$2
      shift 2
      ;;
    --workflow-sha)
      [ "$#" -ge 2 ] || fail '--workflow-sha requires a value'
      workflow_sha=$2
      shift 2
      ;;
    --repository)
      [ "$#" -ge 2 ] || fail '--repository requires a value'
      repository=$2
      shift 2
      ;;
    --digest)
      [ "$#" -ge 2 ] || fail '--digest requires a value'
      digest=$2
      shift 2
      ;;
    --platform)
      [ "$#" -ge 2 ] || fail '--platform requires a value'
      platform=$2
      shift 2
      ;;
    --help|-h)
      printf '%s\n' 'Usage: verify-stable-reuse-handoff.sh --verification FILE --cosign FILE --source-sha SHA --run-id ID --run-attempt NUMBER --workflow-ref REF --workflow-sha SHA --repository REPOSITORY --digest DIGEST --platform PLATFORM'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v jq >/dev/null 2>&1 || fail 'jq is required'
[ -f "$verification_file" ] || fail "verification file not found: $verification_file"
[ -f "$cosign_file" ] || fail "Cosign evidence file not found: $cosign_file"
if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid source SHA: $source_sha"
fi
if [[ ! "$workflow_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid workflow SHA: $workflow_sha"
fi
if [[ ! "$run_id" =~ ^[1-9][0-9]*$ ]]; then
  fail "invalid run ID: $run_id"
fi
if [[ ! "$run_attempt" =~ ^[1-9][0-9]*$ ]]; then
  fail "invalid run attempt: $run_attempt"
fi
if [[ ! "$repository" =~ ^[A-Za-z0-9._-]+(:[0-9]+)?(/[A-Za-z0-9._-]+)+$ ]]; then
  fail "invalid repository: $repository"
fi
if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  fail "invalid digest: $digest"
fi
case "$platform" in
  linux/amd64|linux/arm64) ;;
  *) fail "unsupported platform: $platform" ;;
esac

jq -e \
  --arg source_sha "$source_sha" \
  --arg run_id "$run_id" \
  --arg run_attempt "$run_attempt" \
  --arg workflow_ref "$workflow_ref" \
  --arg workflow_sha "$workflow_sha" \
  --arg repository "$repository" \
  --arg digest "$digest" \
  --arg platform "$platform" '
  def positive_integer:
    type == "string" and test("^[1-9][0-9]*$");

  .source_sha == $source_sha and
  .workflow_ref == $workflow_ref and
  .workflow_sha == $workflow_sha and
  .actions_run.run_id == $run_id and
  (.actions_run.run_attempt | positive_integer) and
  ((.actions_run.run_attempt | tonumber) <= ($run_attempt | tonumber)) and
  (.actions_run.platform_attempts[$platform] | positive_integer) and
  ((.actions_run.platform_attempts[$platform] | tonumber) <=
    (.actions_run.run_attempt | tonumber)) and
  .image_reference == ($repository + "@" + $digest) and
  (.platforms | keys) == [$platform] and
  (.checks | type) == "object" and
  (.checks | length) > 0 and
  all(.checks[]; . == true)
' "$verification_file" >/dev/null || fail 'verification evidence does not match this stable release handoff'

jq -e 'type == "array" and length > 0' "$cosign_file" >/dev/null ||
  fail 'Cosign evidence contains no verified signatures'
