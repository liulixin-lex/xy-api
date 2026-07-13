#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
verifier=$(cd -- "$script_dir/.." && pwd)/verify-stable-reuse-handoff.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
source_repository='liulixin-lex/xy-api'
source_sha='1111111111111111111111111111111111111111'
workflow_sha='3333333333333333333333333333333333333333'
workflow_ref='liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/heads/main'
canonical_ref='liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/v0.1.11'

printf '[{"verified":true}]\n' > "$temp_dir/cosign.json"

write_verification() {
  local mode=$1
  local attested_run_id=$2
  local attested_attempt=$3
  local attested_ref=$4
  local attested_sha=$5
  local verifier_attempt=${6:-3}
  local identity="https://github.com/${workflow_ref}"
  if [ "$mode" = canonical_tag_run ]; then
    identity="https://github.com/${canonical_ref}"
  fi
  local cosign_sha256
  cosign_sha256=$(sha256sum "$temp_dir/cosign.json" | awk '{print $1}')
  jq -n \
    --arg digest "$digest" \
    --arg source_repository "$source_repository" \
    --arg source_sha "$source_sha" \
    --arg workflow_sha "$workflow_sha" \
    --arg workflow_ref "$workflow_ref" \
    --arg verifier_attempt "$verifier_attempt" \
    --arg mode "$mode" \
    --arg attested_run_id "$attested_run_id" \
    --arg attested_attempt "$attested_attempt" \
    --arg attested_ref "$attested_ref" \
    --arg attested_sha "$attested_sha" \
    --arg identity "$identity" \
    --arg cosign_sha256 "$cosign_sha256" '
    ($source_repository + "/actions/runs/" + $attested_run_id + "/attempts/" + $attested_attempt) as $builder_suffix |
    ("https://github.com/" + $builder_suffix) as $builder |
    {
      schema_version: 1,
      image_reference: ("ghcr.io/liulixin-lex/xy-api@" + $digest),
      source_repository: $source_repository,
      source_sha: $source_sha,
      verifier_run: {
        run_id: "123456789",
        run_attempt: $verifier_attempt,
        workflow_ref: $workflow_ref,
        workflow_sha: $workflow_sha
      },
      attested_runs: {
        "linux/amd64": {
          run_id: $attested_run_id,
          run_attempt: $attested_attempt,
          workflow_ref: $attested_ref,
          workflow_sha: $attested_sha,
          builder_id: $builder,
          mode: $mode
        }
      },
      actions_run: {
        run_id: $attested_run_id,
        run_attempt: $verifier_attempt,
        platform_attempts: {"linux/amd64": $attested_attempt},
        builder_ids: {"linux/amd64": $builder}
      },
      raw_evidence_sha256: {
        sbom: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        provenance: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
      },
      platforms: {
        "linux/amd64": {
          provenance: {
            run_id: $attested_run_id,
            run_attempt: $attested_attempt,
            workflow_ref: $attested_ref,
            workflow_sha: $attested_sha,
            builder_id: $builder
          }
        }
      },
      signature: {
        verified: true,
        certificate_identity: $identity,
        certificate_issuer: "https://token.actions.githubusercontent.com",
        raw_evidence_sha256: $cosign_sha256
      },
      retrieval_binding: {
        immutable_reference: ("ghcr.io/liulixin-lex/xy-api@" + $digest),
        requested_digest: $digest,
        resolved_manifest_digest: $digest,
        sbom_and_provenance_retrieved_by_digest: true
      },
      reuse_policy: {
        mode: $mode,
        attested_run: {
          run_id: $attested_run_id,
          run_attempt: $attested_attempt,
          workflow_ref: $attested_ref,
          workflow_sha: $attested_sha,
          builder_id: $builder,
          mode: $mode
        },
        verifier_run: {
          run_id: "123456789",
          run_attempt: $verifier_attempt,
          workflow_ref: $workflow_ref,
          workflow_sha: $workflow_sha
        }
      },
      checks: {signature: true, provenance: true}
    }
  ' > "$temp_dir/verification.json"
}

run_verifier() {
  "$verifier" \
    --verification "$temp_dir/verification.json" \
    --cosign "$temp_dir/cosign.json" \
    --source-repository "$source_repository" \
    --source-sha "$source_sha" \
    --version v0.1.11 \
    --run-id 123456789 \
    --run-attempt 3 \
    --workflow-ref "$workflow_ref" \
    --workflow-sha "$workflow_sha" \
    --repository ghcr.io/liulixin-lex/xy-api \
    --digest "$digest" \
    --platform linux/amd64
}

expect_failure() {
  local name=$1
  shift
  if "$@" > "$temp_dir/${name}.stdout" 2> "$temp_dir/${name}.stderr"; then
    printf 'expected failure for %s\n' "$name" >&2
    exit 1
  fi
}

write_verification same_run 123456789 1 "$workflow_ref" "$workflow_sha"
run_verifier

write_verification same_run 123456789 4 "$workflow_ref" "$workflow_sha"
expect_failure future-attempt run_verifier

write_verification same_run 123456789 1 "$workflow_ref" "$workflow_sha" 2
expect_failure stale-verifier-run run_verifier

write_verification canonical_tag_run 987654321 4 "$canonical_ref" "$source_sha"
run_verifier

write_verification canonical_tag_run 987654321 4 \
  'liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/heads/old-release' \
  "$source_sha"
expect_failure arbitrary-historical-run run_verifier

write_verification canonical_tag_run 987654321 4 "$canonical_ref" \
  2222222222222222222222222222222222222222
expect_failure historical-workflow-sha run_verifier

write_verification canonical_tag_run 987654321 4 "$canonical_ref" "$source_sha"
jq '
  .attested_runs["linux/amd64"].builder_id =
    "https://github.com/liulixin-lex/xy-api/actions/runs/987654321/attempts/3" |
  .reuse_policy.attested_run.builder_id =
    "https://github.com/liulixin-lex/xy-api/actions/runs/987654321/attempts/3" |
  .platforms["linux/amd64"].provenance.builder_id =
    "https://github.com/liulixin-lex/xy-api/actions/runs/987654321/attempts/3" |
  .actions_run.builder_ids["linux/amd64"] =
    "https://github.com/liulixin-lex/xy-api/actions/runs/987654321/attempts/3"
' "$temp_dir/verification.json" > "$temp_dir/builder-mismatch.json"
mv "$temp_dir/builder-mismatch.json" "$temp_dir/verification.json"
expect_failure builder-mismatch run_verifier

printf '[]\n' > "$temp_dir/cosign.json"
write_verification same_run 123456789 1 "$workflow_ref" "$workflow_sha"
expect_failure no-signature run_verifier
grep -Fq 'contains no verified signatures' "$temp_dir/no-signature.stderr"

printf 'stable reuse handoff verification tests passed\n'
