#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <binary> <vMAJOR.MINOR.PATCH> <redis-mode>" >&2
  exit 2
fi

binary=$1
version=$2
redis_mode=$3

if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "invalid standalone smoke version: $version" >&2
  exit 2
fi
case "$redis_mode" in
  disabled | enabled) ;;
  *)
    echo "invalid Redis mode: $redis_mode" >&2
    exit 2
    ;;
esac
if [ ! -f "$binary" ] || [ -L "$binary" ]; then
  echo "standalone smoke binary is missing or is not a regular file: $binary" >&2
  exit 1
fi

binary_directory=$(cd "$(dirname "$binary")" && pwd)
binary="$binary_directory/$(basename "$binary")"
repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
runtime_directory=$(mktemp -d)
runner_temp=${RUNNER_TEMP:-${TMPDIR:-/tmp}}
app_log="$runner_temp/standalone-${redis_mode}-$$.log"
app_pid=''

cleanup() {
  exit_code=$?
  trap - EXIT
  if [[ "$app_pid" =~ ^[1-9][0-9]*$ ]]; then
    kill "$app_pid" >/dev/null 2>&1 || true
    wait "$app_pid" >/dev/null 2>&1 || true
  fi
  if [ "$exit_code" -ne 0 ] && [ -f "$app_log" ]; then
    tail -n 160 "$app_log" >&2 || true
  fi
  rm -rf "$runtime_directory"
  rm -f "$app_log"
  exit "$exit_code"
}
trap cleanup EXIT

chmod 0755 "$binary"
reported_version=$("$binary" --version | tr -d '\r')
if [ "$reported_version" != "$version" ]; then
  echo "standalone binary reported $reported_version, expected $version" >&2
  exit 1
fi

if [ "$redis_mode" = enabled ]; then
  export REDIS_CONN_STRING=redis://127.0.0.1:6379
else
  unset REDIS_CONN_STRING
fi

(
  cd "$runtime_directory"
  GIN_MODE=release \
    SESSION_SECRET=release-runtime-session-secret-not-for-production \
    PAYMENT_SECRET_KEY=release-runtime-payment-secret-not-for-production \
    "$binary" --port 3000 --log-dir "$runtime_directory/logs"
) >"$app_log" 2>&1 &
app_pid=$!

RUNTIME_SMOKE_EXPECTED_VERSION="$version" \
  "$repository_root/scripts/release/runtime-smoke.sh" http://127.0.0.1:3000
if ! kill -0 "$app_pid" >/dev/null 2>&1; then
  echo 'standalone runtime process exited before acceptance completed' >&2
  exit 1
fi

kill "$app_pid" >/dev/null 2>&1
wait_status=0
wait "$app_pid" || wait_status=$?
if [ "$wait_status" -ne 0 ] && [ "$wait_status" -ne 143 ]; then
  echo "standalone runtime process exited unexpectedly with status $wait_status" >&2
  exit 1
fi
app_pid=''

if grep --extended-regexp --ignore-case \
  'fatal|panic|migration failed|failed to initialize' "$app_log"; then
  echo 'standalone runtime logs contain a fatal startup marker' >&2
  exit 1
fi

echo "standalone runtime smoke passed with Redis $redis_mode"
