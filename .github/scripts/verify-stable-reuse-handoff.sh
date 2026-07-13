#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'stable reuse handoff verification failed: %s\n' "$1" >&2
  exit 1
}

verification_file=''
cosign_file=''
source_sha=''
source_repository=''
version=''
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
    --source-repository)
      [ "$#" -ge 2 ] || fail '--source-repository requires a value'
      source_repository=$2
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || fail '--version requires a value'
      version=$2
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
      printf '%s\n' 'Usage: verify-stable-reuse-handoff.sh --verification FILE --cosign FILE --source-repository OWNER/REPOSITORY --source-sha SHA --version TAG --run-id ID --run-attempt NUMBER --workflow-ref REF --workflow-sha SHA --repository REPOSITORY --digest DIGEST --platform PLATFORM'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

command -v jq >/dev/null 2>&1 || fail 'jq is required'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required'
[ -f "$verification_file" ] || fail "verification file not found: $verification_file"
[ -f "$cosign_file" ] || fail "Cosign evidence file not found: $cosign_file"
if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid source SHA: $source_sha"
fi
if [[ ! "$source_repository" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid source repository: $source_repository"
fi
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "invalid stable version: $version"
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
workflow_prefix="$source_repository/.github/workflows/docker-build.yml@refs/"
case "$workflow_ref" in
  "${workflow_prefix}heads/"?*|"${workflow_prefix}tags/"?*) ;;
  *) fail "workflow ref is outside the stable release workflow: $workflow_ref" ;;
esac
canonical_workflow_ref="${workflow_prefix}tags/${version}"
certificate_issuer='https://token.actions.githubusercontent.com'
cosign_sha256=$(sha256sum "$cosign_file" | awk '{print $1}')

jq -e \
  --arg source_repository "$source_repository" \
  --arg source_sha "$source_sha" \
  --arg canonical_workflow_ref "$canonical_workflow_ref" \
  --arg run_id "$run_id" \
  --arg run_attempt "$run_attempt" \
  --arg workflow_ref "$workflow_ref" \
  --arg workflow_sha "$workflow_sha" \
  --arg repository "$repository" \
  --arg digest "$digest" \
  --arg platform "$platform" \
  --arg certificate_issuer "$certificate_issuer" \
  --arg cosign_sha256 "$cosign_sha256" '
  def positive_integer:
    type == "string" and test("^[1-9][0-9]*$");

  .attested_runs[$platform] as $attested |
  .reuse_policy.mode as $mode |
  (if $mode == "same_run" then
    $attested.run_id == $run_id and
    ($attested.run_attempt | positive_integer) and
    ($attested.run_attempt | tonumber) <= ($run_attempt | tonumber) and
    $attested.workflow_ref == $workflow_ref and
    $attested.workflow_sha == $workflow_sha and
    .signature.certificate_identity == ("https://github.com/" + $workflow_ref)
  elif $mode == "canonical_tag_run" then
    $attested.run_id != $run_id and
    ($attested.run_attempt | positive_integer) and
    $attested.workflow_ref == $canonical_workflow_ref and
    $attested.workflow_sha == $source_sha and
    .signature.certificate_identity == ("https://github.com/" + $canonical_workflow_ref)
  else
    false
  end) and
  .source_repository == $source_repository and
  .source_sha == $source_sha and
  .schema_version == 1 and
  .verifier_run == {
    run_id: $run_id,
    run_attempt: $run_attempt,
    workflow_ref: $workflow_ref,
    workflow_sha: $workflow_sha
  } and
  .reuse_policy.verifier_run == .verifier_run and
  .reuse_policy.attested_run == $attested and
  ($attested.run_id | positive_integer) and
  ($attested.workflow_ref | type) == "string" and
  ($attested.workflow_sha | type == "string" and test("^[0-9a-f]{40}$")) and
  $attested.builder_id ==
    ("https://github.com/" + $source_repository + "/actions/runs/" +
      $attested.run_id + "/attempts/" + $attested.run_attempt) and
  .image_reference == ($repository + "@" + $digest) and
  (.platforms | keys) == [$platform] and
  (.attested_runs | keys) == [$platform] and
  .platforms[$platform].provenance.run_id == $attested.run_id and
  .platforms[$platform].provenance.run_attempt == $attested.run_attempt and
  .platforms[$platform].provenance.workflow_ref == $attested.workflow_ref and
  .platforms[$platform].provenance.workflow_sha == $attested.workflow_sha and
  .platforms[$platform].provenance.builder_id == $attested.builder_id and
  .actions_run.platform_attempts[$platform] == $attested.run_attempt and
  .actions_run.builder_ids[$platform] == $attested.builder_id and
  .actions_run.run_id == $attested.run_id and
  .actions_run.run_attempt == $run_attempt and
  (.raw_evidence_sha256.sbom | type == "string" and test("^[0-9a-f]{64}$")) and
  (.raw_evidence_sha256.provenance | type == "string" and test("^[0-9a-f]{64}$")) and
  .signature.verified == true and
  .signature.certificate_issuer == $certificate_issuer and
  .signature.raw_evidence_sha256 == $cosign_sha256 and
  .retrieval_binding == {
    immutable_reference: ($repository + "@" + $digest),
    requested_digest: $digest,
    resolved_manifest_digest: $digest,
    sbom_and_provenance_retrieved_by_digest: true
  } and
  (.checks | type) == "object" and
  (.checks | length) > 0 and
  all(.checks[]; . == true)
' "$verification_file" >/dev/null || fail 'verification evidence does not match this stable release handoff'

jq -e 'type == "array" and length > 0' "$cosign_file" >/dev/null ||
  fail 'Cosign evidence contains no verified signatures'
