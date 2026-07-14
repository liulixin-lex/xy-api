#!/usr/bin/env bash

set -euo pipefail

fail() {
  printf 'stable release finalization failed: %s\n' "$1" >&2
  exit 1
}

waiting() {
  printf 'stable release %s is waiting: %s\n' "$tag" "$1"
  exit 0
}

tag=''
source_sha=''
repository=''
secondary_repository=''
default_branch=''

while [ "$#" -gt 0 ]; do
  case "$1" in
    --tag)
      [ "$#" -ge 2 ] || fail '--tag requires a value'
      tag=$2
      shift 2
      ;;
    --source-sha)
      [ "$#" -ge 2 ] || fail '--source-sha requires a value'
      source_sha=$2
      shift 2
      ;;
    --repository)
      [ "$#" -ge 2 ] || fail '--repository requires a value'
      repository=$2
      shift 2
      ;;
    --secondary-repository)
      [ "$#" -ge 2 ] || fail '--secondary-repository requires a value'
      secondary_repository=$2
      shift 2
      ;;
    --default-branch)
      [ "$#" -ge 2 ] || fail '--default-branch requires a value'
      default_branch=$2
      shift 2
      ;;
    --help|-h)
      printf '%s\n' 'Usage: finalize-stable-release.sh --tag TAG --source-sha SHA --repository REPOSITORY [--secondary-repository REPOSITORY] --default-branch BRANCH'
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

for command in cmp cosign docker gh git jq sha256sum; do
  command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  fail "tag must use stable lowercase v semver: $tag"
fi
if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  fail "invalid source SHA: $source_sha"
fi
if [[ ! "$repository" =~ ^[A-Za-z0-9._-]+(:[0-9]+)?(/[A-Za-z0-9._-]+)+$ ]]; then
  fail "invalid repository: $repository"
fi
if [ -n "$secondary_repository" ] &&
  [[ ! "$secondary_repository" =~ ^[A-Za-z0-9._-]+(:[0-9]+)?(/[A-Za-z0-9._-]+)+$ ]]; then
  fail "invalid secondary repository: $secondary_repository"
fi
if [[ ! "$default_branch" =~ ^[A-Za-z0-9._/-]+$ ]] || [[ "$default_branch" == .* ]] || [[ "$default_branch" == */.* ]]; then
  fail "invalid default branch: $default_branch"
fi
if [[ ! "${GITHUB_REPOSITORY:-}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid GITHUB_REPOSITORY: ${GITHUB_REPOSITORY:-}"
fi

max_attempts=${STABLE_FINALIZER_MAX_ATTEMPTS:-6}
retry_delay_seconds=${STABLE_FINALIZER_RETRY_DELAY_SECONDS:-2}
if [[ ! "$max_attempts" =~ ^[1-9][0-9]*$ ]]; then
  fail "invalid STABLE_FINALIZER_MAX_ATTEMPTS: $max_attempts"
fi
if [[ ! "$retry_delay_seconds" =~ ^[0-9]+$ ]]; then
  fail "invalid STABLE_FINALIZER_RETRY_DELAY_SECONDS: $retry_delay_seconds"
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT
source_repository=$GITHUB_REPOSITORY
source_uri="https://github.com/${source_repository}"
workflow_prefix="${source_repository}/.github/workflows/docker-build.yml@refs/"
default_workflow_ref="${workflow_prefix}heads/${default_branch}"
certificate_issuer='https://token.actions.githubusercontent.com'

validate_release_tag() {
  local release_tag=$1
  local expected_sha=$2
  local resolved_sha

  git fetch --force origin "refs/tags/${release_tag}:refs/tags/${release_tag}" >/dev/null
  resolved_sha=$(git rev-list -n 1 "refs/tags/${release_tag}")
  if [ "$resolved_sha" != "$expected_sha" ]; then
    fail "tag $release_tag resolves to $resolved_sha instead of $expected_sha"
  fi
  if ! git merge-base --is-ancestor "$expected_sha" "origin/${default_branch}"; then
    fail "release commit $expected_sha is not an ancestor of origin/$default_branch"
  fi
  if ! git show "${expected_sha}:VERSION" | cmp -s - <(printf '%s\n' "$release_tag"); then
    fail "VERSION at $release_tag does not exactly match the tag"
  fi
}

git fetch --no-tags origin "$default_branch" >/dev/null
if [ "$(git rev-parse HEAD)" != "$source_sha" ]; then
  fail "checked-out commit does not match release source $source_sha"
fi
validate_release_tag "$tag" "$source_sha"

release_file="$temp_dir/release.json"
release_error="$temp_dir/release.error"
read_release_view() {
  rm -f "$release_file" "$release_error"
  if gh release view "$tag" \
    --repo "$GITHUB_REPOSITORY" \
    --json apiUrl,isDraft,isPrerelease,tagName,assets > "$release_file" 2> "$release_error"; then
    return 0
  fi
  if grep -Fxq 'release not found' "$release_error" || grep -Eq '\(HTTP 404\)$' "$release_error"; then
    return 1
  fi
  cat "$release_error" >&2
  fail "could not read GitHub Release $tag"
}

release_ready=false
for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  if read_release_view; then
    release_ready=true
    break
  fi
  if [ "$attempt" -lt "$max_attempts" ] && [ "$retry_delay_seconds" -gt 0 ]; then
    sleep $((retry_delay_seconds * attempt))
  fi
done
[ "$release_ready" = true ] || waiting 'the GitHub Release draft has not been created yet'

jq -e --arg tag "$tag" '
  .tagName == $tag and
  (.isDraft | type) == "boolean" and
  .isPrerelease == false and
  (.apiUrl | type == "string" and length > 0) and
  (.assets | type) == "array"
' "$release_file" >/dev/null || fail "GitHub Release $tag is not a valid stable release or draft"
release_is_draft=$(jq -r '.isDraft' "$release_file")

assets_dir="$temp_dir/release-assets"
asset_status=0
assets_ready=false
for ((attempt = 1; attempt <= max_attempts; attempt++)); do
  asset_status=0
  "$script_dir/finalize-release-assets.sh" \
    --tag "$tag" \
    --default-branch "$default_branch" \
    --verify-ready \
    --download-dir "$assets_dir" || asset_status=$?
  case "$asset_status" in
    0)
      assets_ready=true
      break
      ;;
    3)
      if [ "$attempt" -lt "$max_attempts" ] && [ "$retry_delay_seconds" -gt 0 ]; then
        sleep $((retry_delay_seconds * attempt))
      fi
      ;;
    *)
      fail 'the GitHub Release asset inventory is invalid'
      ;;
  esac
done
[ "$assets_ready" = true ] || waiting 'the draft does not yet contain the complete verified 10-asset inventory'

inspect_optional_manifest() {
  local reference=$1
  local output_file=$2
  local error_file="${output_file}.error"

  if docker buildx imagetools inspect "$reference" \
    --format '{{json .Manifest}}' > "$output_file" 2> "$error_file"; then
    rm -f "$error_file"
    return 0
  fi
  if grep -Eiq 'manifest unknown|not found|no such manifest|name unknown' "$error_file"; then
    rm -f "$output_file" "$error_file"
    return 1
  fi
  cat "$error_file" >&2
  fail "could not safely inspect $reference"
}

wait_for_manifest() {
  local reference=$1
  local output_file=$2
  local attempt

  for ((attempt = 1; attempt <= max_attempts; attempt++)); do
    if inspect_optional_manifest "$reference" "$output_file"; then
      return 0
    fi
    if [ "$attempt" -lt "$max_attempts" ] && [ "$retry_delay_seconds" -gt 0 ]; then
      sleep $((retry_delay_seconds * attempt))
    fi
  done
  return 1
}

verify_stable_image() {
  local image_repository=$1
  local image_tag=$2
  local expected_source_sha=$3
  local expected_digest=$4
  local evidence_name=$5
  local evidence_dir="$temp_dir/images/${evidence_name}"
  local tag_reference="${image_repository}:${image_tag}"
  local manifest_file="$evidence_dir/manifest.json"
  local immutable_manifest_file="$evidence_dir/manifest-immutable.json"
  local image_file="$evidence_dir/image.json"
  local sbom_file="$evidence_dir/sbom.json"
  local provenance_file="$evidence_dir/provenance.json"
  local digest
  local immutable_reference

  mkdir -p "$evidence_dir/attestations"
  docker buildx imagetools inspect "$tag_reference" \
    --format '{{json .Manifest}}' > "$manifest_file"
  jq -e \
    --arg amd64 'amd64' \
    --arg arm64 'arm64' '
    .mediaType == "application/vnd.oci.image.index.v1+json" and
    (.digest | test("^sha256:[0-9a-f]{64}$")) and
    (.manifests | length) == 4 and
    all(.manifests[];
      .mediaType == "application/vnd.oci.image.manifest.v1+json" and
      (.digest | test("^sha256:[0-9a-f]{64}$")) and
      .size > 0) and
    ([.manifests[] |
      select(.platform.os == "linux" and
        (.annotations["vnd.docker.reference.type"] // "") != "attestation-manifest") |
      .platform.architecture] | sort) == [$amd64, $arm64] and
    ([.manifests[] |
      select(.annotations["vnd.docker.reference.type"] == "attestation-manifest")] | length) == 2 and
    ([.manifests[] | select(.platform.os == "linux") | .digest] | sort) ==
      ([.manifests[] |
        select(.annotations["vnd.docker.reference.type"] == "attestation-manifest") |
        .annotations["vnd.docker.reference.digest"]] | sort)
  ' "$manifest_file" >/dev/null || fail "$tag_reference is not the expected two-platform attested OCI index"

  digest=$(jq -r '.digest' "$manifest_file")
  if [ -n "$expected_digest" ] && [ "$digest" != "$expected_digest" ]; then
    fail "$tag_reference changed digest ($digest != $expected_digest)"
  fi
  immutable_reference="${image_repository}@${digest}"
  docker buildx imagetools inspect "$immutable_reference" \
    --format '{{json .Manifest}}' > "$immutable_manifest_file"
  jq -S . "$manifest_file" > "$evidence_dir/manifest.normalized.json"
  jq -S . "$immutable_manifest_file" > "$evidence_dir/manifest-immutable.normalized.json"
  cmp -s "$evidence_dir/manifest.normalized.json" "$evidence_dir/manifest-immutable.normalized.json" ||
    fail "$tag_reference changed while its immutable digest was being verified"

  docker buildx imagetools inspect "$immutable_reference" \
    --format '{{json .Image}}' > "$image_file"
  jq -e \
    --arg version "$image_tag" \
    --arg revision "$expected_source_sha" '
    (keys | sort) == ["linux/amd64", "linux/arm64"] and
    all(to_entries[];
      .value.os == "linux" and
      .value.architecture == (.key | split("/")[1]) and
      .value.config.Labels["org.opencontainers.image.version"] == $version and
      .value.config.Labels["org.opencontainers.image.revision"] == $revision)
  ' "$image_file" >/dev/null || fail "$tag_reference has invalid platform or OCI release labels"

  while IFS= read -r attestation_digest; do
    local attestation_file="$evidence_dir/attestations/${attestation_digest#sha256:}.json"
    docker buildx imagetools inspect --raw \
      "${image_repository}@${attestation_digest}" > "$attestation_file"
    jq -e '
      (.layers | type) == "array" and
      ([.layers[].annotations["in-toto.io/predicate-type"]] as $types |
        all(.layers[];
          .mediaType == "application/vnd.in-toto+json" and
          (.digest | test("^sha256:[0-9a-f]{64}$")) and
          .size > 0) and
        ($types | index("https://spdx.dev/Document")) != null and
        ($types | index("https://slsa.dev/provenance/v1")) != null)
    ' "$attestation_file" >/dev/null || fail "$tag_reference has an invalid attestation manifest"
  done < <(jq -r '.manifests[] |
    select(.annotations["vnd.docker.reference.type"] == "attestation-manifest") |
    .digest' "$manifest_file")

  docker buildx imagetools inspect "$immutable_reference" \
    --format '{{json .SBOM}}' > "$sbom_file"
  docker buildx imagetools inspect "$immutable_reference" \
    --format '{{json .Provenance}}' > "$provenance_file"

  jq -e '
    def nonempty_string: type == "string" and length > 0;
    def valid_package:
      (.SPDXID? | nonempty_string) and
      (.name? | nonempty_string) and
      (.downloadLocation? | nonempty_string) and
      ((.filesAnalyzed? | type) == "boolean") and
      (.licenseConcluded? | nonempty_string) and
      (.licenseDeclared? | nonempty_string) and
      (.copyrightText? | nonempty_string);

    type == "object" and
    (keys | sort) == ["linux/amd64", "linux/arm64"] and
    all(.[];
      (.SPDX | type) == "object" and
      .SPDX.spdxVersion == "SPDX-2.3" and
      .SPDX.dataLicense == "CC0-1.0" and
      .SPDX.SPDXID == "SPDXRef-DOCUMENT" and
      (.SPDX.name | nonempty_string) and
      (.SPDX.documentNamespace | type == "string" and test("^https?://")) and
      (.SPDX.creationInfo.created | type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T")) and
      (.SPDX.creationInfo.creators | type) == "array" and
      (.SPDX.creationInfo.creators | length) > 0 and
      all(.SPDX.creationInfo.creators[]; nonempty_string) and
      (.SPDX.packages | type) == "array" and
      (.SPDX.packages | length) > 0 and
      all(.SPDX.packages[]; valid_package))
  ' "$sbom_file" >/dev/null || fail "$tag_reference has an invalid or empty SPDX-2.3 SBOM"

  jq -e \
    --arg build_type 'https://github.com/moby/buildkit/blob/master/docs/attestations/slsa-definitions.md' \
    --arg source_repository "$source_repository" \
    --arg source_sha "$expected_source_sha" \
    --arg source_uri "$source_uri" \
    --arg canonical_workflow_ref "${workflow_prefix}tags/${image_tag}" \
    --arg default_workflow_ref "$default_workflow_ref" '
    def nonempty_string: type == "string" and length > 0;
    def positive_integer: type == "string" and test("^[1-9][0-9]*$");
    def revision_claims:
      [
        .SLSA.buildDefinition.externalParameters.request.args["label:org.opencontainers.image.revision"],
        .SLSA.buildDefinition.externalParameters.request.args["vcs:revision"],
        .SLSA.buildDefinition.externalParameters.request.root.request.args["label:org.opencontainers.image.revision"],
        .SLSA.buildDefinition.externalParameters.request.root.request.args["vcs:revision"],
        .SLSA.runDetails.metadata.buildkit_metadata.vcs.revision
      ] | map(select(type == "string" and length > 0));
    def source_claims:
      [
        .SLSA.buildDefinition.externalParameters.request.args["label:org.opencontainers.image.source"],
        .SLSA.buildDefinition.externalParameters.request.args["vcs:source"],
        .SLSA.buildDefinition.externalParameters.request.root.request.args["vcs:source"],
        .SLSA.runDetails.metadata.buildkit_metadata.vcs.source
      ] | map(select(type == "string" and length > 0));

    type == "object" and
    (keys | sort) == ["linux/amd64", "linux/arm64"] and
    all(to_entries[];
      .key as $platform |
      .value |
      .SLSA.buildDefinition.internalParameters as $internal |
      .SLSA.buildDefinition.buildType == $build_type and
      $internal.github_repository == $source_repository and
      ($internal.github_run_id | positive_integer) and
      ($internal.github_run_attempt | positive_integer) and
      $internal.github_job == "build_single_arch" and
      (($internal.github_workflow_ref == $canonical_workflow_ref and
        $internal.github_workflow_sha == $source_sha) or
       ($internal.github_workflow_ref == $default_workflow_ref and
        ($internal.github_workflow_sha | type == "string" and test("^[0-9a-f]{40}$")))) and
      $internal.github_runner_environment == "github-hosted" and
      $internal.github_runner_os == "Linux" and
      $internal.github_runner_arch ==
        (if $platform == "linux/amd64" then "X64" else "ARM64" end) and
      .SLSA.runDetails.builder.id ==
        ($source_uri + "/actions/runs/" + $internal.github_run_id +
          "/attempts/" + $internal.github_run_attempt) and
      .SLSA.runDetails.metadata.buildkit_completeness.request == true and
      (.SLSA.runDetails.metadata.invocationId | nonempty_string) and
      (.SLSA.runDetails.metadata.startedOn | nonempty_string) and
      (.SLSA.runDetails.metadata.finishedOn | nonempty_string) and
      (revision_claims | length) >= 2 and
      all(revision_claims[]; . == $source_sha) and
      (source_claims | length) >= 2 and
      all(source_claims[]; . == $source_uri))
  ' "$provenance_file" >/dev/null || fail "$tag_reference has invalid BuildKit SLSA provenance"

  while IFS= read -r provenance_workflow_sha; do
    [ -n "$provenance_workflow_sha" ] || continue
    if ! git cat-file -e "${provenance_workflow_sha}^{commit}" 2>/dev/null; then
      git fetch --no-tags origin "$provenance_workflow_sha" >/dev/null
    fi
    if ! git merge-base --is-ancestor "$provenance_workflow_sha" "origin/${default_branch}"; then
      fail "$tag_reference was built by an untrusted default-branch workflow commit"
    fi
    git cat-file -e "${provenance_workflow_sha}:.github/workflows/docker-build.yml" 2>/dev/null ||
      fail "$tag_reference provenance workflow commit does not contain docker-build.yml"
  done < <(jq -r \
    --arg default_workflow_ref "$default_workflow_ref" '
    .[] |
    .SLSA.buildDefinition.internalParameters |
    select(.github_workflow_ref == $default_workflow_ref) |
    .github_workflow_sha
  ' "$provenance_file" | LC_ALL=C sort -u)

  if cosign verify \
    --certificate-identity "https://github.com/${workflow_prefix}tags/${image_tag}" \
    --certificate-oidc-issuer "$certificate_issuer" \
    --output json \
    "$immutable_reference" > "$evidence_dir/cosign.json" 2> "$evidence_dir/cosign.error"; then
    :
  elif cosign verify \
    --certificate-identity "https://github.com/${default_workflow_ref}" \
    --certificate-oidc-issuer "$certificate_issuer" \
    --output json \
    "$immutable_reference" > "$evidence_dir/cosign.json" 2> "$evidence_dir/cosign.error"; then
    :
  else
    cat "$evidence_dir/cosign.error" >&2 || true
    fail "$tag_reference has no trusted stable Docker workflow signature"
  fi
  jq -e 'type == "array" and length > 0' "$evidence_dir/cosign.json" >/dev/null ||
    fail "$tag_reference Cosign verification returned no signatures"

  VERIFIED_DIGEST=$digest
}

read_latest_claims() {
  local image_repository=$1
  local evidence_name=$2
  local evidence_dir="$temp_dir/latest/${evidence_name}"
  local manifest_file="$evidence_dir/manifest.json"
  local image_file="$evidence_dir/image.json"
  local -a versions
  local -a revisions

  mkdir -p "$evidence_dir"
  if ! inspect_optional_manifest "${image_repository}:latest" "$manifest_file"; then
    CURRENT_LATEST_DIGEST=''
    CURRENT_LATEST_VERSION=''
    CURRENT_LATEST_SOURCE_SHA=''
    return 0
  fi
  CURRENT_LATEST_DIGEST=$(jq -r '.digest // ""' "$manifest_file")
  if [[ ! "$CURRENT_LATEST_DIGEST" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    fail "${image_repository}:latest returned an invalid digest"
  fi
  docker buildx imagetools inspect "${image_repository}:latest" \
    --format '{{json .Image}}' > "$image_file"
  mapfile -t versions < <(jq -r '[.[] | .config.Labels["org.opencontainers.image.version"] // ""] | unique[]' "$image_file")
  mapfile -t revisions < <(jq -r '[.[] | .config.Labels["org.opencontainers.image.revision"] // ""] | unique[]' "$image_file")
  if [ "${#versions[@]}" -ne 1 ] ||
    [[ ! "${versions[0]}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    fail "${image_repository}:latest does not expose one stable version label"
  fi
  if [ "${#revisions[@]}" -ne 1 ] || [[ ! "${revisions[0]}" =~ ^[0-9a-f]{40}$ ]]; then
    fail "${image_repository}:latest does not expose one source revision label"
  fi
  CURRENT_LATEST_VERSION=${versions[0]}
  CURRENT_LATEST_SOURCE_SHA=${revisions[0]}
}

repositories=("$repository")
repository_names=(primary)
if [ -n "$secondary_repository" ]; then
  repositories+=("$secondary_repository")
  repository_names+=(secondary)
fi

declare -A candidate_digests
declare -A promote_latest
declare -A expected_latest_digests
declare -A expected_latest_versions
declare -A expected_latest_source_shas
declare -A planned_current_digests
declare -A planned_current_versions
declare -A planned_current_source_shas

for index in "${!repositories[@]}"; do
  image_repository=${repositories[$index]}
  repository_name=${repository_names[$index]}
  candidate_manifest="$temp_dir/${repository_name}-candidate-preflight.json"
  if ! wait_for_manifest "${image_repository}:${tag}" "$candidate_manifest"; then
    if [ "$release_is_draft" = true ]; then
      waiting "the signed immutable image ${image_repository}:${tag} is not available yet"
    fi
    fail "published GitHub Release $tag is missing ${image_repository}:${tag}"
  fi
  verify_stable_image "$image_repository" "$tag" "$source_sha" '' "${repository_name}-candidate"
  candidate_digests[$repository_name]=$VERIFIED_DIGEST
  if [ "$repository_name" = secondary ] &&
    [ "${candidate_digests[secondary]}" != "${candidate_digests[primary]}" ]; then
    fail 'stable candidate digests disagree across registries'
  fi

  read_latest_claims "$image_repository" "$repository_name"
  planned_current_digests[$repository_name]=$CURRENT_LATEST_DIGEST
  planned_current_versions[$repository_name]=$CURRENT_LATEST_VERSION
  planned_current_source_shas[$repository_name]=$CURRENT_LATEST_SOURCE_SHA
  if [ -n "$CURRENT_LATEST_DIGEST" ]; then
    validate_release_tag "$CURRENT_LATEST_VERSION" "$CURRENT_LATEST_SOURCE_SHA"
    verify_stable_image \
      "$image_repository" \
      "$CURRENT_LATEST_VERSION" \
      "$CURRENT_LATEST_SOURCE_SHA" \
      "$CURRENT_LATEST_DIGEST" \
      "${repository_name}-current-latest"
  fi

  plan_file="$temp_dir/${repository_name}-latest-plan.env"
  resolver_args=(
    --repository "$image_repository"
    --tag "$tag"
    --candidate-digest "${candidate_digests[$repository_name]}"
    --output "$plan_file"
  )
  if [ -n "$CURRENT_LATEST_DIGEST" ]; then
    resolver_args+=(
      --trusted-current-version "$CURRENT_LATEST_VERSION"
      --trusted-current-digest "$CURRENT_LATEST_DIGEST"
    )
  fi
  "$script_dir/resolve-stable-latest.sh" "${resolver_args[@]}"

  plan_promote=''
  plan_expected_digest=''
  plan_current_digest=''
  plan_current_version=''
  while IFS='=' read -r key value; do
    case "$key" in
      promote_latest) plan_promote=$value ;;
      expected_latest_digest) plan_expected_digest=$value ;;
      current_latest_digest) plan_current_digest=$value ;;
      current_latest_version) plan_current_version=$value ;;
      *) fail "unexpected stable latest plan key: $key" ;;
    esac
  done < "$plan_file"
  case "$plan_promote" in
    true|false) ;;
    *) fail 'stable latest plan omitted a valid promotion decision' ;;
  esac
  if [[ ! "$plan_expected_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    fail 'stable latest plan omitted a valid expected digest'
  fi
  if [ "$plan_current_digest" != "$CURRENT_LATEST_DIGEST" ] ||
    [ "$plan_current_version" != "$CURRENT_LATEST_VERSION" ]; then
    fail 'stable latest plan did not preserve the trusted current state'
  fi
  promote_latest[$repository_name]=$plan_promote
  expected_latest_digests[$repository_name]=$plan_expected_digest
  if [ "$plan_expected_digest" = "${candidate_digests[$repository_name]}" ]; then
    expected_latest_versions[$repository_name]=$tag
  expected_latest_source_shas[$repository_name]=$source_sha
  else
    expected_latest_versions[$repository_name]=$CURRENT_LATEST_VERSION
    expected_latest_source_shas[$repository_name]=$CURRENT_LATEST_SOURCE_SHA
  fi
done

# Recheck every mutable input immediately before any registry tag is changed.
# This closes the normal eventual-consistency window between planning and promotion.
git fetch --no-tags origin "$default_branch" >/dev/null
validate_release_tag "$tag" "$source_sha"
for index in "${!repositories[@]}"; do
  image_repository=${repositories[$index]}
  repository_name=${repository_names[$index]}
  candidate_version_digest=$(docker buildx imagetools inspect \
    "${image_repository}:${tag}" --format '{{.Manifest.Digest}}')
  if [ "$candidate_version_digest" != "${candidate_digests[$repository_name]}" ]; then
    fail "${image_repository}:${tag} changed before latest promotion"
  fi

  read_latest_claims "$image_repository" "$repository_name"
  if [ "$CURRENT_LATEST_DIGEST" != "${planned_current_digests[$repository_name]}" ] ||
    [ "$CURRENT_LATEST_VERSION" != "${planned_current_versions[$repository_name]}" ] ||
    [ "$CURRENT_LATEST_SOURCE_SHA" != "${planned_current_source_shas[$repository_name]}" ]; then
    fail "${image_repository}:latest changed after planning"
  fi
  if [ -n "$CURRENT_LATEST_DIGEST" ]; then
    verify_stable_image \
      "$image_repository" \
      "$CURRENT_LATEST_VERSION" \
      "$CURRENT_LATEST_SOURCE_SHA" \
      "$CURRENT_LATEST_DIGEST" \
      "${repository_name}-preflight-latest"
  fi
done

for index in "${!repositories[@]}"; do
  image_repository=${repositories[$index]}
  repository_name=${repository_names[$index]}
  if [ "${promote_latest[$repository_name]}" = true ]; then
    docker buildx imagetools create \
      -t "${image_repository}:latest" \
      "${image_repository}@${candidate_digests[$repository_name]}"
  fi
done

for index in "${!repositories[@]}"; do
  image_repository=${repositories[$index]}
  repository_name=${repository_names[$index]}
  latest_manifest="$temp_dir/${repository_name}-latest-final.json"
  if ! wait_for_manifest "${image_repository}:latest" "$latest_manifest"; then
    fail "${image_repository}:latest did not become available after finalization"
  fi
  latest_digest=$(jq -r '.digest // ""' "$latest_manifest")
  if [ "$latest_digest" != "${expected_latest_digests[$repository_name]}" ]; then
    fail "${image_repository}:latest changed unexpectedly after promotion"
  fi
  verify_stable_image \
    "$image_repository" \
    "${expected_latest_versions[$repository_name]}" \
    "${expected_latest_source_shas[$repository_name]}" \
    "$latest_digest" \
    "${repository_name}-final-latest"
  version_digest=$(docker buildx imagetools inspect "${image_repository}:${tag}" --format '{{.Manifest.Digest}}')
  if [ "$version_digest" != "${candidate_digests[$repository_name]}" ]; then
    fail "${image_repository}:${tag} moved before GitHub Release publication"
  fi
done

git fetch --no-tags origin "$default_branch" >/dev/null
validate_release_tag "$tag" "$source_sha"
"$script_dir/finalize-release-assets.sh" \
  --tag "$tag" \
  --default-branch "$default_branch"
printf 'stable release %s is complete across GitHub Release and signed container registries\n' "$tag"
