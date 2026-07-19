#!/usr/bin/env bash

set -Eeuo pipefail

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <image-reference> <expected-version> <amd64|arm64>" >&2
  exit 2
fi

image_reference=$1
expected_version=$2
expected_arch=$3

case "$expected_arch" in
  amd64 | arm64) ;;
  *)
    echo "unsupported image architecture: $expected_arch" >&2
    exit 2
    ;;
esac

for command_name in curl docker jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command is not installed: $command_name" >&2
    exit 1
  fi
done

container_name="new-api-release-smoke-${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-0}-${expected_arch}-${RANDOM}"
response_file=$(mktemp)
container_started=false
smoke_secret=$(
  printf '%s:%s:%s:%s\n' \
    "${GITHUB_RUN_ID:-local}" \
    "${GITHUB_RUN_ATTEMPT:-0}" \
    "$expected_arch" \
    "$RANDOM" |
    sha256sum |
    awk '{ print $1 }'
)

# shellcheck disable=SC2317 # Invoked by the EXIT trap.
cleanup() {
  exit_code=$?
  if [ "$container_started" = true ]; then
    if [ "$exit_code" -ne 0 ]; then
      echo "release smoke container logs ($expected_arch):" >&2
      docker logs "$container_name" >&2 || true
    fi
    docker rm --force "$container_name" >/dev/null 2>&1 || true
  fi
  rm -f "$response_file"
}
trap cleanup EXIT

docker pull --platform "linux/$expected_arch" "$image_reference"

actual_arch=$(docker image inspect --format '{{.Architecture}}' "$image_reference")
if [ "$actual_arch" != "$expected_arch" ]; then
  echo "image architecture mismatch: expected $expected_arch, got $actual_arch" >&2
  exit 1
fi

reported_version=$(
  docker run --rm --platform "linux/$expected_arch" "$image_reference" --version |
    tr -d '\r' |
    tail -n 1
)
if [ "$reported_version" != "$expected_version" ]; then
  echo "image version mismatch: expected $expected_version, got $reported_version" >&2
  exit 1
fi

docker run \
  --detach \
  --name "$container_name" \
  --platform "linux/$expected_arch" \
  --publish 127.0.0.1::3000 \
  --tmpfs /data:rw,nosuid,nodev,noexec,size=268435456 \
  --env GIN_MODE=release \
  --env "SESSION_SECRET=$smoke_secret" \
  --env "PAYMENT_SECRET_KEY=$smoke_secret" \
  "$image_reference" >/dev/null
container_started=true

published_port=""
for _ in $(seq 1 30); do
  published_port=$(
    docker port "$container_name" 3000/tcp 2>/dev/null |
      awk -F: 'NR == 1 { print $NF }'
  )
  if [ -n "$published_port" ]; then
    break
  fi
  sleep 1
done
if [ -z "$published_port" ]; then
  echo "container did not publish port 3000" >&2
  exit 1
fi

status_url="http://127.0.0.1:${published_port}/api/status"
for _ in $(seq 1 120); do
  if [ "$(docker inspect --format '{{.State.Running}}' "$container_name" 2>/dev/null || true)" != true ]; then
    echo "container exited before becoming ready" >&2
    exit 1
  fi

  if curl \
    --fail \
    --silent \
    --show-error \
    --noproxy '*' \
    --max-time 5 \
    "$status_url" \
    --output "$response_file" 2>/dev/null; then
    if jq --exit-status \
      --arg expected_version "$expected_version" \
      '.success == true and .data.version == $expected_version' \
      "$response_file" >/dev/null; then
      echo "verified $image_reference ($expected_arch, $expected_version) via /api/status"
      exit 0
    fi
  fi

  sleep 2
done

echo "image did not return the expected /api/status payload within 240 seconds" >&2
if [ -s "$response_file" ]; then
  echo "last response:" >&2
  sed -n '1,40p' "$response_file" >&2
fi
exit 1
