#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
finalizer=$(cd -- "$script_dir/.." && pwd)/finalize-stable-release.sh
fixture_dir="$script_dir/fixtures"
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT

source_sha='1111111111111111111111111111111111111111'
older_sha='0000000000000000000000000000000000000000'
newer_sha='2222222222222222222222222222222222222222'
candidate_digest='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
older_digest='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
newer_digest='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'

mkdir -p "$temp_dir/bin" "$temp_dir/assets" "$temp_dir/state"

cat > "$temp_dir/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$1" in
  fetch)
    exit 0
    ;;
  rev-parse)
    if [ "$2" = HEAD ]; then
      printf '%s\n' "$MOCK_SOURCE_SHA"
    else
      value=${2%\^\{commit\}}
      printf '%s\n' "$value"
    fi
    ;;
  rev-list)
    ref=${!#}
    tag=${ref##*/}
    case "$tag" in
      v0.1.10) printf '%s\n' "$MOCK_OLDER_SHA" ;;
      v0.1.11) printf '%s\n' "$MOCK_SOURCE_SHA" ;;
      v0.1.12) printf '%s\n' "$MOCK_NEWER_SHA" ;;
      *) exit 1 ;;
    esac
    ;;
  merge-base)
    exit 0
    ;;
  show)
    sha=${2%%:*}
    case "$sha" in
      "$MOCK_OLDER_SHA") printf 'v0.1.10\n' ;;
      "$MOCK_SOURCE_SHA") printf 'v0.1.11\n' ;;
      "$MOCK_NEWER_SHA") printf 'v0.1.12\n' ;;
      *) exit 1 ;;
    esac
    ;;
  cat-file)
    exit 0
    ;;
  *)
    echo "unexpected git invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$temp_dir/bin/git"

cat > "$temp_dir/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf 'gh %s\n' "$*" >> "$MOCK_CALLS"

if [ "$1" = release ] && [ "$2" = view ]; then
  if [ "$(cat "$MOCK_STATE_DIR/release.state")" = missing ]; then
    echo 'release not found' >&2
    exit 1
  fi
  if [ "$3" != 'v0.1.11' ]; then
    jq -n --arg tag "$3" '{
      apiUrl: "https://api.github.test/repos/liulixin-lex/xy-api/releases/102",
      isDraft: false,
      isPrerelease: false,
      tagName: $tag,
      assets: []
    }'
    exit 0
  fi
  cat "$MOCK_RELEASE_JSON"
  exit 0
fi
if [ "$1" = release ] && [ "$2" = download ]; then
  output_dir=''
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --dir ]; then
      output_dir=$2
      break
    fi
    shift
  done
  [ -n "$output_dir" ]
  cp "$MOCK_ASSET_DIR"/* "$output_dir/"
  exit 0
fi
if [ "$1" = release ] && [ "$2" = edit ]; then
  printf '%s\n' "$3" > "$MOCK_STATE_DIR/github-latest.tag"
  exit 0
fi
if [ "$1" = api ]; then
  if printf '%s\n' "$*" | grep -Fq -- '--method PATCH'; then
    jq '.isDraft = false' "$MOCK_RELEASE_JSON" > "$MOCK_RELEASE_JSON.tmp"
    mv "$MOCK_RELEASE_JSON.tmp" "$MOCK_RELEASE_JSON"
    printf '{"tag_name":"v0.1.11","draft":false,"prerelease":false}\n'
    exit 0
  fi
  endpoint=${!#}
  if [[ "$endpoint" == */releases/latest ]]; then
    latest_tag=$(cat "$MOCK_STATE_DIR/github-latest.tag")
    if [ -z "$latest_tag" ]; then
      echo 'gh: Not Found (HTTP 404)' >&2
      exit 1
    fi
    printf '{"tag_name":"%s"}\n' "$latest_tag"
    exit 0
  fi
fi

echo "unexpected gh invocation: $*" >&2
exit 1
EOF
chmod +x "$temp_dir/bin/gh"

cat > "$temp_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf 'docker %s\n' "$*" >> "$MOCK_CALLS"
[ "$1" = buildx ] && [ "$2" = imagetools ]

state_value() {
  cat "$MOCK_STATE_DIR/$1"
}

resolve_reference() {
  local reference=$1
  local digest=''
  local version=''
  local revision=''

  case "$reference" in
    *@sha256:*)
      digest=${reference##*@}
      case "$digest" in
        "$MOCK_CANDIDATE_DIGEST") version=v0.1.11; revision=$MOCK_SOURCE_SHA ;;
        "$MOCK_OLDER_DIGEST") version=v0.1.10; revision=$MOCK_OLDER_SHA ;;
        "$MOCK_NEWER_DIGEST") version=v0.1.12; revision=$MOCK_NEWER_SHA ;;
        *) version=attestation; revision='' ;;
      esac
      ;;
    *:latest)
      if [ "$(state_value latest.state)" = missing ]; then
        echo 'manifest unknown' >&2
        return 1
      fi
      digest=$(state_value latest.digest)
      version=$(state_value latest.version)
      revision=$(state_value latest.sha)
      ;;
    *:v0.1.10)
      digest=$MOCK_OLDER_DIGEST
      version=v0.1.10
      revision=$MOCK_OLDER_SHA
      ;;
    *:v0.1.11)
      if [ "$(state_value candidate.state)" = missing ]; then
        echo 'manifest unknown' >&2
        return 1
      fi
      digest=$MOCK_CANDIDATE_DIGEST
      version=v0.1.11
      revision=$MOCK_SOURCE_SHA
      ;;
    *:v0.1.12)
      digest=$MOCK_NEWER_DIGEST
      version=v0.1.12
      revision=$MOCK_NEWER_SHA
      ;;
    *)
      echo "unknown image reference: $reference" >&2
      return 1
      ;;
  esac

  RESOLVED_DIGEST=$digest
  RESOLVED_VERSION=$version
  RESOLVED_REVISION=$revision
}

manifest_json() {
  jq -n --arg digest "$RESOLVED_DIGEST" '{
    schemaVersion: 2,
    mediaType: "application/vnd.oci.image.index.v1+json",
    digest: $digest,
    size: 1600,
    manifests: [
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
        size: 1200,
        platform: {architecture: "amd64", os: "linux"}
      },
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
        size: 800,
        annotations: {
          "vnd.docker.reference.digest": "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
          "vnd.docker.reference.type": "attestation-manifest"
        },
        platform: {architecture: "unknown", os: "unknown"}
      },
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
        size: 1200,
        platform: {architecture: "arm64", os: "linux"}
      },
      {
        mediaType: "application/vnd.oci.image.manifest.v1+json",
        digest: "sha256:9999999999999999999999999999999999999999999999999999999999999999",
        size: 800,
        annotations: {
          "vnd.docker.reference.digest": "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
          "vnd.docker.reference.type": "attestation-manifest"
        },
        platform: {architecture: "unknown", os: "unknown"}
      }
    ]
  }'
}

image_json() {
  jq -n --arg version "$RESOLVED_VERSION" --arg revision "$RESOLVED_REVISION" '{
    "linux/amd64": {
      os: "linux",
      architecture: "amd64",
      config: {Labels: {
        "org.opencontainers.image.version": $version,
        "org.opencontainers.image.revision": $revision
      }}
    },
    "linux/arm64": {
      os: "linux",
      architecture: "arm64",
      config: {Labels: {
        "org.opencontainers.image.version": $version,
        "org.opencontainers.image.revision": $revision
      }}
    }
  }'
}

provenance_json() {
  jq \
    --arg source_sha "$RESOLVED_REVISION" \
    --arg workflow_ref "liulixin-lex/xy-api/.github/workflows/docker-build.yml@refs/tags/${RESOLVED_VERSION}" '
    with_entries(
      .value.SLSA.buildDefinition.externalParameters.request.args["label:org.opencontainers.image.revision"] = $source_sha |
      .value.SLSA.buildDefinition.externalParameters.request.root.request.args["vcs:revision"] = $source_sha |
      .value.SLSA.runDetails.metadata.buildkit_metadata.vcs.revision = $source_sha |
      .value.SLSA.buildDefinition.internalParameters.github_workflow_ref = $workflow_ref |
      .value.SLSA.buildDefinition.internalParameters.github_workflow_sha = $source_sha
    )
  ' "$MOCK_PROVENANCE_FIXTURE"
}

case "$3" in
  inspect)
    if [ "$4" = --raw ]; then
      cat <<'JSON'
{"schemaVersion":2,"layers":[{"mediaType":"application/vnd.in-toto+json","digest":"sha256:7777777777777777777777777777777777777777777777777777777777777777","size":100,"annotations":{"in-toto.io/predicate-type":"https://spdx.dev/Document"}},{"mediaType":"application/vnd.in-toto+json","digest":"sha256:8888888888888888888888888888888888888888888888888888888888888888","size":100,"annotations":{"in-toto.io/predicate-type":"https://slsa.dev/provenance/v1"}}]}
JSON
      exit 0
    fi
    reference=$4
    format=$6
    resolve_reference "$reference"
    case "$format" in
      '{{json .Manifest}}') manifest_json ;;
      '{{.Manifest.Digest}}') printf '%s\n' "$RESOLVED_DIGEST" ;;
      '{{json .Image}}') image_json ;;
      '{{json .SBOM}}') cat "$MOCK_SBOM_FIXTURE" ;;
      '{{json .Provenance}}') provenance_json ;;
      *) echo "unexpected inspect format: $format" >&2; exit 1 ;;
    esac
    ;;
  create)
    target=''
    source=''
    shift 3
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -t)
          target=$2
          shift 2
          ;;
        *)
          source=$1
          shift
          ;;
      esac
    done
    if [[ "$target" != *:latest ]]; then
      echo "unexpected create target: $target" >&2
      exit 1
    fi
    digest=${source##*@}
    case "$digest" in
      "$MOCK_CANDIDATE_DIGEST") version=v0.1.11; revision=$MOCK_SOURCE_SHA ;;
      "$MOCK_NEWER_DIGEST") version=v0.1.12; revision=$MOCK_NEWER_SHA ;;
      "$MOCK_OLDER_DIGEST") version=v0.1.10; revision=$MOCK_OLDER_SHA ;;
      *) exit 1 ;;
    esac
    printf 'existing\n' > "$MOCK_STATE_DIR/latest.state"
    printf '%s\n' "$digest" > "$MOCK_STATE_DIR/latest.digest"
    printf '%s\n' "$version" > "$MOCK_STATE_DIR/latest.version"
    printf '%s\n' "$revision" > "$MOCK_STATE_DIR/latest.sha"
    ;;
  *)
    echo "unexpected docker invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$temp_dir/bin/docker"

cat > "$temp_dir/bin/cosign" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf 'cosign %s\n' "$*" >> "$MOCK_CALLS"
target=${!#}
digest=${target##*@}
if [ -s "$MOCK_STATE_DIR/untrusted.digest" ] &&
  [ "$digest" = "$(cat "$MOCK_STATE_DIR/untrusted.digest")" ]; then
  echo 'no matching signatures' >&2
  exit 1
fi
printf '[{"critical":{"image":{"docker-manifest-digest":"%s"}}}]\n' "$digest"
EOF
chmod +x "$temp_dir/bin/cosign"

export PATH="$temp_dir/bin:$PATH"
export GITHUB_API_URL='https://api.github.test'
export GITHUB_REPOSITORY='liulixin-lex/xy-api'
export MOCK_ASSET_DIR="$temp_dir/assets"
export MOCK_CALLS="$temp_dir/calls"
export MOCK_CANDIDATE_DIGEST="$candidate_digest"
export MOCK_NEWER_DIGEST="$newer_digest"
export MOCK_NEWER_SHA="$newer_sha"
export MOCK_OLDER_DIGEST="$older_digest"
export MOCK_OLDER_SHA="$older_sha"
export MOCK_PROVENANCE_FIXTURE="$fixture_dir/release-provenance.valid.json"
export MOCK_RELEASE_JSON="$temp_dir/release.json"
export MOCK_SBOM_FIXTURE="$fixture_dir/release-sbom.valid.json"
export MOCK_SOURCE_SHA="$source_sha"
export MOCK_STATE_DIR="$temp_dir/state"
export STABLE_FINALIZER_MAX_ATTEMPTS=1
export STABLE_FINALIZER_RETRY_DELAY_SECONDS=0

create_core_assets() {
  printf 'linux-amd64\n' > "$MOCK_ASSET_DIR/new-api-v0.1.11"
  printf 'linux-arm64\n' > "$MOCK_ASSET_DIR/new-api-arm64-v0.1.11"
  printf 'macos\n' > "$MOCK_ASSET_DIR/new-api-macos-v0.1.11"
  printf 'windows\n' > "$MOCK_ASSET_DIR/new-api-v0.1.11.exe"
  (
    cd "$MOCK_ASSET_DIR"
    sha256sum new-api-arm64-v0.1.11 new-api-v0.1.11 > checksums-linux.txt
    sha256sum new-api-macos-v0.1.11 > checksums-macos.txt
    sha256sum -b new-api-v0.1.11.exe > checksums-windows.txt
  )
}

create_electron_assets() {
  printf 'electron-portable\n' > "$MOCK_ASSET_DIR/New-API-App.0.1.11.exe"
  printf 'electron-setup\n' > "$MOCK_ASSET_DIR/New-API-App.Setup.0.1.11.exe"
  (
    cd "$MOCK_ASSET_DIR"
    sha256sum -b ./New-API-App.0.1.11.exe ./New-API-App.Setup.0.1.11.exe > \
      checksums-electron-windows.txt
  )
}

write_release_json() {
  local draft=$1
  find "$MOCK_ASSET_DIR" -mindepth 1 -maxdepth 1 -type f -printf '%f\n' |
    LC_ALL=C sort |
    jq -Rsc --argjson draft "$draft" '
      split("\n") |
      map(select(length > 0)) |
      {
        apiUrl: "https://api.github.test/repos/liulixin-lex/xy-api/releases/101",
        isDraft: $draft,
        isPrerelease: false,
        tagName: "v0.1.11",
        assets: map({name: .})
      }
    ' > "$MOCK_RELEASE_JSON"
}

reset_state() {
  rm -f "$MOCK_ASSET_DIR"/* "$MOCK_STATE_DIR"/untrusted.digest
  : > "$MOCK_CALLS"
  printf 'existing\n' > "$MOCK_STATE_DIR/release.state"
  printf 'existing\n' > "$MOCK_STATE_DIR/candidate.state"
  printf 'missing\n' > "$MOCK_STATE_DIR/latest.state"
  : > "$MOCK_STATE_DIR/latest.digest"
  : > "$MOCK_STATE_DIR/latest.version"
  : > "$MOCK_STATE_DIR/latest.sha"
  : > "$MOCK_STATE_DIR/github-latest.tag"
}

set_latest() {
  local version=$1
  local digest=$2
  local sha=$3
  printf 'existing\n' > "$MOCK_STATE_DIR/latest.state"
  printf '%s\n' "$digest" > "$MOCK_STATE_DIR/latest.digest"
  printf '%s\n' "$version" > "$MOCK_STATE_DIR/latest.version"
  printf '%s\n' "$sha" > "$MOCK_STATE_DIR/latest.sha"
}

run_finalizer() {
  "$finalizer" \
    --tag v0.1.11 \
    --source-sha "$source_sha" \
    --repository ghcr.io/liulixin-lex/xy-api \
    --default-branch main
}

reset_state
printf 'missing\n' > "$MOCK_STATE_DIR/release.state"
run_finalizer > "$temp_dir/docker-first.stdout"
grep -Fq 'Release draft has not been created' "$temp_dir/docker-first.stdout"
if grep -Fq 'imagetools create' "$MOCK_CALLS"; then
  echo 'Docker-first waiting must not promote latest' >&2
  exit 1
fi

reset_state
create_core_assets
write_release_json true
run_finalizer > "$temp_dir/core-first.stdout"
grep -Fq 'complete verified 10-asset inventory' "$temp_dir/core-first.stdout"
if grep -Fq 'imagetools create' "$MOCK_CALLS"; then
  echo 'core-first waiting must not promote latest' >&2
  exit 1
fi

reset_state
create_electron_assets
write_release_json true
run_finalizer > "$temp_dir/electron-first.stdout"
grep -Fq 'complete verified 10-asset inventory' "$temp_dir/electron-first.stdout"
if grep -Fq 'imagetools create' "$MOCK_CALLS"; then
  echo 'Electron-first waiting must not promote latest' >&2
  exit 1
fi

reset_state
create_core_assets
create_electron_assets
write_release_json true
printf 'missing\n' > "$MOCK_STATE_DIR/candidate.state"
run_finalizer > "$temp_dir/image-last-wait.stdout"
grep -Fq 'signed immutable image' "$temp_dir/image-last-wait.stdout"

printf 'existing\n' > "$MOCK_STATE_DIR/candidate.state"
set_latest v0.1.10 "$older_digest" "$older_sha"
run_finalizer > "$temp_dir/image-last-complete.stdout"
grep -Fq 'complete across GitHub Release' "$temp_dir/image-last-complete.stdout"
[ "$(cat "$MOCK_STATE_DIR/latest.digest")" = "$candidate_digest" ]
[ "$(cat "$MOCK_STATE_DIR/latest.version")" = v0.1.11 ]
grep -Fq 'gh api --method PATCH' "$MOCK_CALLS"
grep -Fq 'gh release edit v0.1.11' "$MOCK_CALLS"

: > "$MOCK_CALLS"
run_finalizer > "$temp_dir/repeated.stdout"
grep -Fq 'complete across GitHub Release' "$temp_dir/repeated.stdout"
if grep -Fq 'imagetools create' "$MOCK_CALLS" || grep -Fq 'gh api --method PATCH' "$MOCK_CALLS"; then
  echo 'repeated finalization must be a no-op' >&2
  exit 1
fi

reset_state
create_core_assets
create_electron_assets
write_release_json true
set_latest v0.1.12 "$newer_digest" "$newer_sha"
printf 'v0.1.12\n' > "$MOCK_STATE_DIR/github-latest.tag"
run_finalizer > "$temp_dir/newer.stdout"
[ "$(cat "$MOCK_STATE_DIR/latest.digest")" = "$newer_digest" ]
if grep -Fq 'imagetools create' "$MOCK_CALLS"; then
  echo 'trusted newer latest must be preserved' >&2
  exit 1
fi
grep -Fq 'refusing to move latest backward' "$temp_dir/newer.stdout"

reset_state
create_core_assets
create_electron_assets
write_release_json true
set_latest v0.1.12 "$newer_digest" "$newer_sha"
printf '%s\n' "$newer_digest" > "$MOCK_STATE_DIR/untrusted.digest"
if run_finalizer > "$temp_dir/untrusted.stdout" 2> "$temp_dir/untrusted.stderr"; then
  echo 'expected unsigned newer latest to fail closed' >&2
  exit 1
fi
grep -Fq 'no trusted stable Docker workflow signature' "$temp_dir/untrusted.stderr"
if grep -Fq 'gh api --method PATCH' "$MOCK_CALLS"; then
  echo 'unsigned latest failure must not publish the Release' >&2
  exit 1
fi

reset_state
create_core_assets
create_electron_assets
write_release_json true
set_latest v0.1.11 "$older_digest" "$source_sha"
if run_finalizer > "$temp_dir/conflict.stdout" 2> "$temp_dir/conflict.stderr"; then
  echo 'expected same-version digest conflict to fail closed' >&2
  exit 1
fi
grep -Fq 'changed digest' "$temp_dir/conflict.stderr"
if grep -Fq 'gh api --method PATCH' "$MOCK_CALLS"; then
  echo 'same-version conflict must not publish the Release' >&2
  exit 1
fi

printf 'stable cross-domain finalization tests passed\n'
