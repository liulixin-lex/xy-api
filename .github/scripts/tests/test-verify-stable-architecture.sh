#!/usr/bin/env bash

# Workflow assertions intentionally match literal shell expressions.
# shellcheck disable=SC2016

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
verifier=$(cd -- "$script_dir/.." && pwd)/verify-stable-architecture.sh
fixture_dir="$script_dir/fixtures"
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
image_digest='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
attestation_digest='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'

jq -n \
  --arg digest "$digest" \
  --arg image_digest "$image_digest" \
  --arg attestation_digest "$attestation_digest" '
  {
    mediaType: "application/vnd.oci.image.index.v1+json",
    digest: $digest,
    manifests: [
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: $image_digest,
        size: 100,
        platform: {os: "linux", architecture: "amd64"}
      },
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: $attestation_digest,
        size: 100,
        platform: {os: "unknown", architecture: "unknown"},
        annotations: {
          "vnd.docker.reference.type": "attestation-manifest",
          "vnd.docker.reference.digest": $image_digest
        }
      }
    ]
  }
' > "$temp_dir/manifest.json"

jq -n '{
  os: "linux",
  architecture: "amd64",
  config: {Labels: {
    "org.opencontainers.image.version": "v0.1.11",
    "org.opencontainers.image.revision": "1111111111111111111111111111111111111111"
  }}
}' > "$temp_dir/image.json"

jq -n '{layers: [
  {
    mediaType: "application/vnd.in-toto+json",
    digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    size: 100,
    annotations: {"in-toto.io/predicate-type": "https://spdx.dev/Document"}
  },
  {
    mediaType: "application/vnd.in-toto+json",
    digest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
    size: 100,
    annotations: {"in-toto.io/predicate-type": "https://slsa.dev/provenance/v1"}
  }
]}' > "$temp_dir/attestation.json"

jq '."linux/amd64"' \
  "$fixture_dir/release-sbom.valid.json" > "$temp_dir/sbom.json"
jq '
  ."linux/amd64" |
  .SLSA.buildDefinition.internalParameters.github_run_attempt = "1" |
  .SLSA.runDetails.builder.id =
    "https://github.com/liulixin-lex/xy-api/actions/runs/123456789/attempts/1"
' \
  "$fixture_dir/release-provenance.valid.json" > "$temp_dir/provenance.json"

mkdir -p "$temp_dir/bin"
cat > "$temp_dir/bin/cosign" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'cosign %s\n' "$*" >> "$MOCK_CALLS"
identity=''
while [ "$#" -gt 0 ]; do
  if [ "$1" = --certificate-identity ]; then
    identity=$2
    break
  fi
  shift
done
if [ "${MOCK_COSIGN_STATE:-valid}" = invalid ] ||
  { [ -n "${MOCK_ALLOWED_IDENTITY:-}" ] && [ "$identity" != "$MOCK_ALLOWED_IDENTITY" ]; }; then
  echo 'signature verification failed' >&2
  exit 1
fi
printf '[{"verified":true}]\n'
EOF

cat > "$temp_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s\n' "$*" >> "$MOCK_CALLS"
if [ "$4" = --raw ]; then
  cat "$MOCK_ATTESTATION"
  exit 0
fi
case "${*: -1}" in
  '{{json .Manifest}}') cat "$MOCK_MANIFEST" ;;
  '{{json .Image}}') cat "$MOCK_IMAGE" ;;
  '{{json .SBOM}}') cat "$MOCK_SBOM" ;;
  '{{json .Provenance}}') cat "$MOCK_PROVENANCE" ;;
  *) echo "unexpected docker invocation: $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$temp_dir/bin/cosign" "$temp_dir/bin/docker"

export PATH="$temp_dir/bin:$PATH"
export MOCK_CALLS="$temp_dir/calls"
export MOCK_MANIFEST="$temp_dir/manifest.json"
export MOCK_IMAGE="$temp_dir/image.json"
export MOCK_ATTESTATION="$temp_dir/attestation.json"
export MOCK_SBOM="$temp_dir/sbom.json"
export MOCK_PROVENANCE="$temp_dir/provenance.json"
export MOCK_ALLOWED_IDENTITY='https://github.com/liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/v0.1.11'

run_verifier() {
  local output_dir=$1
  local selected_run_id=${2:-123456789}
  local selected_run_attempt=${3:-2}
  local selected_workflow_ref=${4:-liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/v0.1.11}
  local selected_workflow_sha=${5:-3333333333333333333333333333333333333333}
  "$verifier" \
    --repository ghcr.io/liulixin-lex/xy-api \
    --digest "$digest" \
    --platform linux/amd64 \
    --version v0.1.11 \
    --source-repository liulixin-lex/xy-api \
    --source-sha 1111111111111111111111111111111111111111 \
    --run-id "$selected_run_id" \
    --run-attempt "$selected_run_attempt" \
    --workflow-ref "$selected_workflow_ref" \
    --workflow-sha "$selected_workflow_sha" \
    --output-dir "$output_dir"
}

: > "$MOCK_CALLS"
run_verifier "$temp_dir/valid-output"
jq -e '
  (.platforms | keys) == ["linux/amd64"] and
  .actions_run.run_attempt == "2" and
  .platforms["linux/amd64"].provenance.run_attempt == "1" and
  .retrieval_binding.immutable_reference == .image_reference and
  .retrieval_binding.requested_digest ==
    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" and
  .retrieval_binding.sbom_and_provenance_retrieved_by_digest == true and
  all(.checks[]; . == true)
' "$temp_dir/valid-output/verification.json" >/dev/null
[ "$(sed -n '1p' "$MOCK_CALLS")" = "cosign verify --certificate-identity https://github.com/liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/v0.1.11 --certificate-oidc-issuer https://token.actions.githubusercontent.com --output json ghcr.io/liulixin-lex/xy-api@$digest" ]
grep -Fq "docker buildx imagetools inspect ghcr.io/liulixin-lex/xy-api@$digest --format {{json .SBOM}}" "$MOCK_CALLS"
grep -Fq "docker buildx imagetools inspect ghcr.io/liulixin-lex/xy-api@$digest --format {{json .Provenance}}" "$MOCK_CALLS"

jq '.digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"' \
  "$temp_dir/manifest.json" > "$temp_dir/wrong-digest-manifest.json"
export MOCK_MANIFEST="$temp_dir/wrong-digest-manifest.json"
: > "$MOCK_CALLS"
if run_verifier "$temp_dir/wrong-digest-output" > "$temp_dir/wrong-digest.stdout" 2> "$temp_dir/wrong-digest.stderr"; then
  echo 'expected manifest digest mismatch to fail' >&2
  exit 1
fi
grep -Fq 'signed single-platform OCI image' "$temp_dir/wrong-digest.stderr"
if grep -Fq '{{json .SBOM}}' "$MOCK_CALLS" || grep -Fq '{{json .Provenance}}' "$MOCK_CALLS"; then
  echo 'attestations must not be read after immutable manifest digest mismatch' >&2
  exit 1
fi
export MOCK_MANIFEST="$temp_dir/manifest.json"

export MOCK_COSIGN_STATE=invalid
: > "$MOCK_CALLS"
if run_verifier "$temp_dir/invalid-signature-output" > "$temp_dir/invalid-signature.stdout" 2> "$temp_dir/invalid-signature.stderr"; then
  echo 'expected invalid signature to fail' >&2
  exit 1
fi
[ "$(wc -l < "$MOCK_CALLS")" -eq 1 ]
grep -Fq 'no signature from the current or canonical stable workflow identity' \
  "$temp_dir/invalid-signature.stderr"
unset MOCK_COSIGN_STATE

jq '
  .SLSA.buildDefinition.internalParameters.github_run_id = "987654321" |
  .SLSA.buildDefinition.internalParameters.github_run_attempt = "4" |
  .SLSA.buildDefinition.internalParameters.github_workflow_sha =
    "1111111111111111111111111111111111111111" |
  .SLSA.runDetails.builder.id =
    "https://github.com/liulixin-lex/xy-api/actions/runs/987654321/attempts/4"
' "$temp_dir/provenance.json" > "$temp_dir/historical-provenance.json"
export MOCK_PROVENANCE="$temp_dir/historical-provenance.json"
: > "$MOCK_CALLS"
run_verifier \
  "$temp_dir/historical-output" \
  555555555 \
  2 \
  liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/heads/main \
  4444444444444444444444444444444444444444
jq -e '
  .reuse_policy.mode == "canonical_tag_run" and
  .verifier_run.run_id == "555555555" and
  .attested_runs["linux/amd64"].run_id == "987654321" and
  .signature.certificate_identity ==
    "https://github.com/liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/v0.1.11"
' "$temp_dir/historical-output/verification.json" >/dev/null
[ "$(grep -c '^cosign ' "$MOCK_CALLS")" -eq 2 ]

jq '
  .SLSA.buildDefinition.internalParameters.github_workflow_ref =
    "liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/heads/old-release"
' "$temp_dir/historical-provenance.json" > "$temp_dir/arbitrary-historical-provenance.json"
export MOCK_PROVENANCE="$temp_dir/arbitrary-historical-provenance.json"
if run_verifier \
  "$temp_dir/arbitrary-historical-output" \
  555555555 \
  2 \
  liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/heads/main \
  4444444444444444444444444444444444444444 \
  > "$temp_dir/arbitrary-historical.stdout" 2> "$temp_dir/arbitrary-historical.stderr"; then
  echo 'expected arbitrary historical workflow to fail' >&2
  exit 1
fi
grep -Fq 'allowed source run' "$temp_dir/arbitrary-historical.stderr"

jq '
  .SLSA.buildDefinition.internalParameters.github_workflow_sha =
    "2222222222222222222222222222222222222222"
' "$temp_dir/historical-provenance.json" > "$temp_dir/historical-wrong-sha.json"
export MOCK_PROVENANCE="$temp_dir/historical-wrong-sha.json"
if run_verifier \
  "$temp_dir/historical-wrong-sha-output" \
  555555555 \
  2 \
  liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/heads/main \
  4444444444444444444444444444444444444444 \
  > "$temp_dir/historical-wrong-sha.stdout" 2> "$temp_dir/historical-wrong-sha.stderr"; then
  echo 'expected historical workflow SHA mismatch to fail' >&2
  exit 1
fi
grep -Fq 'allowed source run' "$temp_dir/historical-wrong-sha.stderr"
export MOCK_PROVENANCE="$temp_dir/provenance.json"

jq '.manifests[0].platform.architecture = "arm64"' \
  "$temp_dir/manifest.json" > "$temp_dir/wrong-platform-manifest.json"
export MOCK_MANIFEST="$temp_dir/wrong-platform-manifest.json"
if run_verifier "$temp_dir/wrong-platform-output" > "$temp_dir/wrong-platform.stdout" 2> "$temp_dir/wrong-platform.stderr"; then
  echo 'expected wrong platform to fail' >&2
  exit 1
fi
grep -Fq 'single-platform OCI image' "$temp_dir/wrong-platform.stderr"
export MOCK_MANIFEST="$temp_dir/manifest.json"

jq '.config.Labels["org.opencontainers.image.revision"] = "2222222222222222222222222222222222222222"' \
  "$temp_dir/image.json" > "$temp_dir/wrong-revision-image.json"
export MOCK_IMAGE="$temp_dir/wrong-revision-image.json"
if run_verifier "$temp_dir/wrong-revision-output" > "$temp_dir/wrong-revision.stdout" 2> "$temp_dir/wrong-revision.stderr"; then
  echo 'expected wrong revision label to fail' >&2
  exit 1
fi
grep -Fq 'version/revision labels' "$temp_dir/wrong-revision.stderr"

workflow="$repo_root/.github/workflows/docker-build.yml"
grep -Fq 'immutable_reference="${repository}@${digest}"' "$workflow"
grep -Fq 'docker buildx imagetools inspect "$immutable_reference" --format '\''{{json .SBOM}}'\''' "$workflow"
grep -Fq 'docker buildx imagetools inspect "$immutable_reference" --format '\''{{json .Provenance}}'\''' "$workflow"
grep -Fq 'sbom_and_provenance_retrieved_by_digest: true' "$workflow"

printf 'stable architecture verification tests passed\n'
