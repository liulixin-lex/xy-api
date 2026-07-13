#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
verifier=$(cd -- "$script_dir/.." && pwd)/verify-stable-reuse-handoff.sh
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
source_sha='1111111111111111111111111111111111111111'
workflow_sha='3333333333333333333333333333333333333333'
workflow_ref='liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/v0.1.11'

write_verification() {
  local verifier_attempt=$1
  local build_attempt=$2
  jq -n \
    --arg digest "$digest" \
    --arg source_sha "$source_sha" \
    --arg workflow_sha "$workflow_sha" \
    --arg workflow_ref "$workflow_ref" \
    --arg verifier_attempt "$verifier_attempt" \
    --arg build_attempt "$build_attempt" '{
      schema_version: 1,
      image_reference: ("ghcr.io/liulixin-lex/xy-api@" + $digest),
      source_sha: $source_sha,
      workflow_ref: $workflow_ref,
      workflow_sha: $workflow_sha,
      actions_run: {
        run_id: "123456789",
        run_attempt: $verifier_attempt,
        platform_attempts: {"linux/amd64": $build_attempt}
      },
      platforms: {"linux/amd64": {}},
      checks: {signature: true, provenance: true}
    }' > "$temp_dir/verification.json"
}

run_verifier() {
  local current_attempt=$1
  "$verifier" \
    --verification "$temp_dir/verification.json" \
    --cosign "$temp_dir/cosign.json" \
    --source-sha "$source_sha" \
    --run-id 123456789 \
    --run-attempt "$current_attempt" \
    --workflow-ref "$workflow_ref" \
    --workflow-sha "$workflow_sha" \
    --repository ghcr.io/liulixin-lex/xy-api \
    --digest "$digest" \
    --platform linux/amd64
}

printf '[{"verified":true}]\n' > "$temp_dir/cosign.json"

write_verification 2 1
run_verifier 3

write_verification 4 1
if run_verifier 3 > "$temp_dir/future.stdout" 2> "$temp_dir/future.stderr"; then
  echo 'expected future verifier attempt to fail' >&2
  exit 1
fi
grep -Fq 'does not match this stable release handoff' "$temp_dir/future.stderr"

write_verification 2 3
if run_verifier 3 > "$temp_dir/build-future.stdout" 2> "$temp_dir/build-future.stderr"; then
  echo 'expected build attempt newer than verifier attempt to fail' >&2
  exit 1
fi

write_verification 2 1
jq '.actions_run.run_id = "987654321"' \
  "$temp_dir/verification.json" > "$temp_dir/wrong-run.json"
mv "$temp_dir/wrong-run.json" "$temp_dir/verification.json"
if run_verifier 3 > "$temp_dir/wrong-run.stdout" 2> "$temp_dir/wrong-run.stderr"; then
  echo 'expected wrong run ID to fail' >&2
  exit 1
fi

printf '[]\n' > "$temp_dir/cosign.json"
write_verification 2 1
if run_verifier 3 > "$temp_dir/no-signature.stdout" 2> "$temp_dir/no-signature.stderr"; then
  echo 'expected empty Cosign evidence to fail' >&2
  exit 1
fi
grep -Fq 'contains no verified signatures' "$temp_dir/no-signature.stderr"

printf 'stable reuse handoff verification tests passed\n'
