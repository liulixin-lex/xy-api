#!/usr/bin/env bash

set -Eeuo pipefail
umask 077

usage() {
  cat >&2 <<'EOF'
usage: runtime-smoke.sh [base-url]

Runs the v0.2 runtime acceptance smoke against an already-started new-api
instance. A fresh instance is initialized automatically with an in-memory
generated root password. An initialized instance requires all of:

  RUNTIME_SMOKE_ALLOW_EXISTING_INSTANCE=1
  RUNTIME_SMOKE_CONFIRM_EXCLUSIVE_TARGET=1
  RUNTIME_SMOKE_USERNAME=<root username>
  RUNTIME_SMOKE_PASSWORD=<root password>

The smoke creates and removes one channel and one API token. Relay calls use a
local OpenAI-compatible mock and never call a payment provider. Existing
instances must be isolated from user traffic while the high-priority mock
channel exists and retain the two consume logs and their small quota charge.
Use a disposable restored database for release validation, never an active
production instance.

Useful optional variables:

  RUNTIME_SMOKE_EXPECTED_VERSION=v0.2.1
  RUNTIME_SMOKE_ALLOW_REMOTE=1              # remote targets must use HTTPS
  RUNTIME_SMOKE_ALLOW_REMOTE_INITIALIZATION=1
  RUNTIME_SMOKE_PAUSE_PERFORMANCE_GATE=1    # initialized disposable targets
  RUNTIME_SMOKE_MOCK_BIND_HOST=127.0.0.1
  RUNTIME_SMOKE_MOCK_BIND_PORT=0             # 0 selects a free port
  RUNTIME_SMOKE_MOCK_ADVERTISE_HOST=127.0.0.1
  RUNTIME_SMOKE_MOCK_PROBE_HOST=127.0.0.1
  RUNTIME_SMOKE_ALLOW_NON_LOOPBACK_MOCK=1    # for host.docker.internal, etc.
  RUNTIME_SMOKE_REQUEST_TIMEOUT=45
  RUNTIME_SMOKE_READY_TIMEOUT=60
EOF
}

if [ "$#" -gt 1 ]; then
  usage
  exit 2
fi

configured_admin_username=${RUNTIME_SMOKE_USERNAME:-}
configured_admin_password=${RUNTIME_SMOKE_PASSWORD:-}
# Do not let the login password propagate through inherited environments or
# process arguments. JSON builders and curl receive it only through stdin, and
# the non-exported shell copy is erased immediately after login succeeds.
unset RUNTIME_SMOKE_PASSWORD

for command_name in curl jq python3; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "runtime smoke failed: required command is not installed: $command_name" >&2
    exit 1
  fi
done

base_url=${1:-${RUNTIME_SMOKE_BASE_URL:-http://127.0.0.1:3000}}
base_url=${base_url%/}
expected_version=${RUNTIME_SMOKE_EXPECTED_VERSION:-}
allow_existing=${RUNTIME_SMOKE_ALLOW_EXISTING_INSTANCE:-0}
confirm_exclusive_target=${RUNTIME_SMOKE_CONFIRM_EXCLUSIVE_TARGET:-0}
allow_remote=${RUNTIME_SMOKE_ALLOW_REMOTE:-0}
allow_remote_initialization=${RUNTIME_SMOKE_ALLOW_REMOTE_INITIALIZATION:-0}
pause_performance_gate=${RUNTIME_SMOKE_PAUSE_PERFORMANCE_GATE:-0}
allow_non_loopback_mock=${RUNTIME_SMOKE_ALLOW_NON_LOOPBACK_MOCK:-0}
request_timeout=${RUNTIME_SMOKE_REQUEST_TIMEOUT:-45}
ready_timeout=${RUNTIME_SMOKE_READY_TIMEOUT:-60}
mock_bind_host=${RUNTIME_SMOKE_MOCK_BIND_HOST:-127.0.0.1}
mock_bind_port=${RUNTIME_SMOKE_MOCK_BIND_PORT:-0}
mock_advertise_host=${RUNTIME_SMOKE_MOCK_ADVERTISE_HOST:-127.0.0.1}
mock_probe_host=${RUNTIME_SMOKE_MOCK_PROBE_HOST:-}

model_name=gpt-3.5-turbo
upstream_key=runtime-smoke-upstream-key-not-a-secret
token_quota=1000000

fail() {
  echo "runtime smoke failed: $*" >&2
  exit 1
}

log() {
  echo "[runtime-smoke] $*"
}

case "$allow_existing" in
  0 | 1) ;;
  *) fail "RUNTIME_SMOKE_ALLOW_EXISTING_INSTANCE must be 0 or 1" ;;
esac
case "$confirm_exclusive_target" in
  0 | 1) ;;
  *) fail "RUNTIME_SMOKE_CONFIRM_EXCLUSIVE_TARGET must be 0 or 1" ;;
esac
case "$allow_remote" in
  0 | 1) ;;
  *) fail "RUNTIME_SMOKE_ALLOW_REMOTE must be 0 or 1" ;;
esac
case "$allow_remote_initialization" in
  0 | 1) ;;
  *) fail "RUNTIME_SMOKE_ALLOW_REMOTE_INITIALIZATION must be 0 or 1" ;;
esac
case "$pause_performance_gate" in
  0 | 1) ;;
  *) fail "RUNTIME_SMOKE_PAUSE_PERFORMANCE_GATE must be 0 or 1" ;;
esac
case "$allow_non_loopback_mock" in
  0 | 1) ;;
  *) fail "RUNTIME_SMOKE_ALLOW_NON_LOOPBACK_MOCK must be 0 or 1" ;;
esac
if ! [[ "$request_timeout" =~ ^[1-9][0-9]*$ ]]; then
  fail "RUNTIME_SMOKE_REQUEST_TIMEOUT must be a positive integer"
fi
if ! [[ "$ready_timeout" =~ ^[1-9][0-9]*$ ]]; then
  fail "RUNTIME_SMOKE_READY_TIMEOUT must be a positive integer"
fi
if ! [[ "$mock_bind_port" =~ ^[0-9]+$ ]] || [ "$mock_bind_port" -gt 65535 ]; then
  fail "RUNTIME_SMOKE_MOCK_BIND_PORT must be between 0 and 65535"
fi

url_info=$(
  python3 - "$base_url" <<'PY'
import ipaddress
import sys
from urllib.parse import urlsplit

parsed = urlsplit(sys.argv[1])
if parsed.scheme not in {"http", "https"}:
    raise SystemExit(1)
if not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
    raise SystemExit(1)

host = parsed.hostname.rstrip(".").lower()
is_loopback = host == "localhost"
if not is_loopback:
    try:
        is_loopback = ipaddress.ip_address(host).is_loopback
    except ValueError:
        is_loopback = False

print(parsed.scheme, "1" if is_loopback else "0")
PY
) || fail "base URL must be an HTTP(S) URL without credentials, query, or fragment"

base_scheme=${url_info%% *}
base_is_loopback=${url_info##* }
base_curl_proxy_args=()
if [ "$base_is_loopback" != 1 ]; then
  if [ "$allow_remote" != 1 ]; then
    fail "non-loopback targets require RUNTIME_SMOKE_ALLOW_REMOTE=1"
  fi
  if [ "$base_scheme" != https ]; then
    fail "non-loopback targets must use HTTPS so credentials and cookies are not exposed"
  fi
else
  base_curl_proxy_args=(--noproxy '*')
fi

mock_hosts_are_loopback=$(
  python3 - "$mock_bind_host" "$mock_advertise_host" <<'PY'
import ipaddress
import sys

results = []
for raw_host in sys.argv[1:]:
    host = raw_host.strip().strip("[]").rstrip(".").lower()
    is_loopback = host == "localhost"
    if not is_loopback:
        try:
            is_loopback = ipaddress.ip_address(host).is_loopback
        except ValueError:
            is_loopback = False
    results.append("1" if is_loopback else "0")
print(" ".join(results))
PY
)
mock_bind_is_loopback=${mock_hosts_are_loopback%% *}
mock_advertise_is_loopback=${mock_hosts_are_loopback##* }
if { [ "$mock_bind_is_loopback" != 1 ] || [ "$mock_advertise_is_loopback" != 1 ]; } && \
   [ "$allow_non_loopback_mock" != 1 ]; then
  fail "a non-loopback mock bind or advertise host requires RUNTIME_SMOKE_ALLOW_NON_LOOPBACK_MOCK=1"
fi

work_dir=$(mktemp -d)
chmod 700 "$work_dir"
cookie_jar="$work_dir/cookies"
response_file="$work_dir/response.json"
stream_file="$work_dir/stream.txt"
token_header_file="$work_dir/token.headers"
token_readonly_header_file="$work_dir/token-readonly.headers"
mock_header_file="$work_dir/mock.headers"
mock_port_file="$work_dir/mock.port"
mock_log_file="$work_dir/mock.log"
cleanup_response_file="$work_dir/cleanup.json"
touch "$cookie_jar" "$response_file" "$stream_file" "$token_header_file" "$token_readonly_header_file" "$mock_header_file" "$mock_log_file"
chmod 600 "$cookie_jar" "$response_file" "$stream_file" "$token_header_file" "$token_readonly_header_file" "$mock_header_file" "$mock_log_file"

user_id=""
channel_id=""
token_id=""
channel_created=false
token_created=false
mock_pid=""
last_http_status=""
admin_password=""
fresh_setup=false
performance_gate_changed=false
performance_monitor_original=""

run_id=$(python3 -c 'import secrets; print(secrets.token_hex(6))')
channel_name="runtime-smoke-$run_id"
token_name="runtime-smoke-$run_id"

best_effort_session_request() {
  local method=$1
  local path=$2
  local output=$3
  local body=${4-}
  local -a curl_args=(
    --silent
    --connect-timeout 2
    --max-time 5
    --request "$method"
    --cookie "$cookie_jar"
    --cookie-jar "$cookie_jar"
    --header "New-Api-User: $user_id"
    --output "$output"
    "${base_curl_proxy_args[@]}"
  )

  if [ "$#" -ge 4 ]; then
    curl_args+=(--header 'Content-Type: application/json')
    curl "${curl_args[@]}" --data-binary @- "$base_url$path" <<<"$body" >/dev/null 2>&1
  else
    curl "${curl_args[@]}" "$base_url$path" >/dev/null 2>&1
  fi
}

cleanup() {
  local exit_code=$?
  trap - EXIT
  set +e

  if [ "$performance_gate_changed" = true ] && [ -n "$user_id" ]; then
    restore_payload=$(jq --compact-output --null-input --argjson value "$performance_monitor_original" \
      '{key:"performance_setting.monitor_enabled",value:$value}' 2>/dev/null)
    best_effort_session_request PUT /api/option/ "$cleanup_response_file" "$restore_payload" || true
  fi

  if [ "$token_created" = true ] && [ -n "$user_id" ]; then
    if ! [[ "$token_id" =~ ^[1-9][0-9]*$ ]]; then
      if best_effort_session_request GET "/api/token/?p=1&page_size=100" "$cleanup_response_file"; then
        token_id=$(jq -r --arg name "$token_name" 'first(.data.items[]? | select(.name == $name) | .id) // empty' "$cleanup_response_file" 2>/dev/null)
      fi
    fi
    if [[ "$token_id" =~ ^[1-9][0-9]*$ ]]; then
      best_effort_session_request DELETE "/api/token/$token_id" "$cleanup_response_file" || true
    fi
  fi

  if [ "$channel_created" = true ] && [ -n "$user_id" ]; then
    if ! [[ "$channel_id" =~ ^[1-9][0-9]*$ ]]; then
      if best_effort_session_request GET "/api/channel/?p=1&page_size=100&id_sort=true" "$cleanup_response_file"; then
        channel_id=$(jq -r --arg name "$channel_name" 'first(.data.items[]? | select(.name == $name) | .id) // empty' "$cleanup_response_file" 2>/dev/null)
      fi
    fi
    if [[ "$channel_id" =~ ^[1-9][0-9]*$ ]]; then
      best_effort_session_request DELETE "/api/channel/$channel_id" "$cleanup_response_file" || true
    fi
  fi

  if [[ "$mock_pid" =~ ^[1-9][0-9]*$ ]]; then
    kill "$mock_pid" >/dev/null 2>&1 || true
    wait "$mock_pid" >/dev/null 2>&1 || true
  fi

  unset admin_password configured_admin_password
  rm -rf "$work_dir"
  exit "$exit_code"
}
trap cleanup EXIT

http_request() {
  local auth_mode=$1
  local method=$2
  local path=$3
  local output=$4
  local body=${5-}
  local has_body=false
  local -a curl_args=(
    --silent
    --show-error
    --compressed
    --connect-timeout 5
    --max-time "$request_timeout"
    --request "$method"
    --output "$output"
    --write-out '%{http_code}'
    --header 'Accept: application/json'
    "${base_curl_proxy_args[@]}"
  )

  if [ "$#" -ge 5 ]; then
    has_body=true
    curl_args+=(--header 'Content-Type: application/json')
  fi

  case "$auth_mode" in
    public)
      curl_args+=(--cookie "$cookie_jar" --cookie-jar "$cookie_jar")
      ;;
    session)
      if ! [[ "$user_id" =~ ^[1-9][0-9]*$ ]]; then
        fail "session request attempted without a validated user id"
      fi
      curl_args+=(
        --cookie "$cookie_jar"
        --cookie-jar "$cookie_jar"
        --header "New-Api-User: $user_id"
      )
      ;;
    token)
      if [ ! -s "$token_header_file" ]; then
        fail "token request attempted before token material was stored"
      fi
      curl_args+=(--header "@$token_header_file")
      ;;
    token_readonly)
      if [ ! -s "$token_readonly_header_file" ]; then
        fail "read-only token request attempted before token material was stored"
      fi
      curl_args+=(--header "@$token_readonly_header_file")
      ;;
    *) fail "unknown request authentication mode: $auth_mode" ;;
  esac

  if [ "$has_body" = true ]; then
    if ! last_http_status=$(curl "${curl_args[@]}" --data-binary @- "$base_url$path" <<<"$body"); then
      fail "$method $path could not reach the target"
    fi
  else
    if ! last_http_status=$(curl "${curl_args[@]}" "$base_url$path"); then
      fail "$method $path could not reach the target"
    fi
  fi
}

expect_http_status() {
  local allowed=" $1 "
  local description=$2
  if [[ "$allowed" != *" $last_http_status "* ]]; then
    fail "$description returned HTTP $last_http_status"
  fi
}

expect_api_success() {
  local file=$1
  local description=$2
  if ! jq --exit-status '.success == true' "$file" >/dev/null 2>&1; then
    fail "$description did not return the success contract"
  fi
}

wait_for_readiness() {
  local attempt
  local status
  for ((attempt = 0; attempt < ready_timeout; attempt++)); do
    if status=$(
      curl \
        --silent \
        --connect-timeout 1 \
        --max-time 2 \
        "${base_curl_proxy_args[@]}" \
        --output "$response_file" \
        --write-out '%{http_code}' \
        "$base_url/api/readiness" 2>/dev/null
    ); then
      if [ "$status" = 200 ] && jq --exit-status '.success == true and .status == "ready"' "$response_file" >/dev/null 2>&1; then
        return
      fi
    fi
    sleep 1
  done
  fail "target did not become ready within ${ready_timeout}s"
}

start_mock() {
  python3 - "$mock_port_file" "$mock_bind_host" "$mock_bind_port" <<'PY' >"$mock_log_file" 2>&1 &
import json
import socket
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

PORT_FILE, BIND_HOST, BIND_PORT = sys.argv[1], sys.argv[2].strip("[]"), int(sys.argv[3])
MODEL = "gpt-3.5-turbo"
AUTHORIZATION = "Bearer runtime-smoke-upstream-key-not-a-secret"


class Handler(BaseHTTPRequestHandler):
    server_version = "new-api-runtime-smoke-mock"
    sys_version = ""

    def log_message(self, _format, *_args):
        return

    def send_json(self, status, payload):
        encoded = json.dumps(payload, separators=(",", ":")).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(encoded)

    def authorized(self):
        return self.headers.get("Authorization") == AUTHORIZATION

    def do_GET(self):
        if urlsplit(self.path).path != "/v1/models":
            self.send_json(404, {"error": {"message": "not found"}})
            return
        if not self.authorized():
            self.send_json(401, {"error": {"message": "unauthorized"}})
            return
        self.send_json(200, {
            "object": "list",
            "data": [{"id": MODEL, "object": "model", "created": 1, "owned_by": "runtime-smoke"}],
        })

    def do_POST(self):
        if urlsplit(self.path).path != "/v1/chat/completions":
            self.send_json(404, {"error": {"message": "not found"}})
            return
        if not self.authorized():
            self.send_json(401, {"error": {"message": "unauthorized"}})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_json(400, {"error": {"message": "invalid request"}})
            return
        if length <= 0 or length > 1024 * 1024:
            self.send_json(400, {"error": {"message": "invalid request"}})
            return
        try:
            request = json.loads(self.rfile.read(length))
        except (json.JSONDecodeError, UnicodeDecodeError):
            self.send_json(400, {"error": {"message": "invalid request"}})
            return
        if request.get("model") != MODEL:
            self.send_json(400, {"error": {"message": "unsupported model"}})
            return

        created = int(time.time())
        if not request.get("stream", False):
            self.send_json(200, {
                "id": "chatcmpl-runtime-smoke",
                "object": "chat.completion",
                "created": created,
                "model": MODEL,
                "choices": [{
                    "index": 0,
                    "message": {"role": "assistant", "content": "runtime smoke ok"},
                    "finish_reason": "stop",
                    "logprobs": None,
                }],
                "usage": {"prompt_tokens": 5, "completion_tokens": 7, "total_tokens": 12},
            })
            return

        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        chunks = [
            {
                "id": "chatcmpl-runtime-smoke-stream",
                "object": "chat.completion.chunk",
                "created": created,
                "model": MODEL,
                "choices": [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": None}],
            },
            {
                "id": "chatcmpl-runtime-smoke-stream",
                "object": "chat.completion.chunk",
                "created": created,
                "model": MODEL,
                "choices": [{"index": 0, "delta": {"content": "runtime smoke stream"}, "finish_reason": None}],
            },
            {
                "id": "chatcmpl-runtime-smoke-stream",
                "object": "chat.completion.chunk",
                "created": created,
                "model": MODEL,
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            },
            {
                "id": "chatcmpl-runtime-smoke-stream",
                "object": "chat.completion.chunk",
                "created": created,
                "model": MODEL,
                "choices": [],
                "usage": {"prompt_tokens": 5, "completion_tokens": 7, "total_tokens": 12},
            },
        ]
        for chunk in chunks:
            encoded = json.dumps(chunk, separators=(",", ":")).encode("utf-8")
            self.wfile.write(b"data: " + encoded + b"\n\n")
            self.wfile.flush()
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
        self.close_connection = True


class SmokeHTTPServer(ThreadingHTTPServer):
    address_family = socket.AF_INET6 if ":" in BIND_HOST else socket.AF_INET


server = SmokeHTTPServer((BIND_HOST, BIND_PORT), Handler)
server.daemon_threads = True
with open(PORT_FILE, "w", encoding="ascii") as port_file:
    port_file.write(str(server.server_address[1]))
    port_file.flush()
server.serve_forever(poll_interval=0.1)
PY
  mock_pid=$!

  local attempt
  for ((attempt = 0; attempt < 100; attempt++)); do
    if [ -s "$mock_port_file" ]; then
      break
    fi
    if ! kill -0 "$mock_pid" >/dev/null 2>&1; then
      fail "local OpenAI mock exited during startup"
    fi
    sleep 0.1
  done
  if [ ! -s "$mock_port_file" ]; then
    fail "local OpenAI mock did not publish a port"
  fi

  local selected_port
  selected_port=$(tr -d '[:space:]' <"$mock_port_file")
  if ! [[ "$selected_port" =~ ^[1-9][0-9]*$ ]] || [ "$selected_port" -gt 65535 ]; then
    fail "local OpenAI mock returned an invalid port"
  fi

  local advertised_host=$mock_advertise_host
  if [[ "$advertised_host" == *:* ]] && [[ "$advertised_host" != \[*\] ]]; then
    advertised_host="[$advertised_host]"
  fi
  mock_base_url="http://$advertised_host:$selected_port"

  local selected_probe_host=$mock_probe_host
  if [ -z "$selected_probe_host" ]; then
    case "$mock_bind_host" in
      0.0.0.0) selected_probe_host=127.0.0.1 ;;
      :: | '[::]') selected_probe_host=::1 ;;
      *) selected_probe_host=$mock_bind_host ;;
    esac
  fi
  if [[ "$selected_probe_host" == *:* ]] && [[ "$selected_probe_host" != \[*\] ]]; then
    selected_probe_host="[$selected_probe_host]"
  fi
  local mock_probe_url="http://$selected_probe_host:$selected_port"

  printf 'Authorization: Bearer %s\n' "$upstream_key" >"$mock_header_file"
  local mock_status
  if ! mock_status=$(
    curl \
      --silent \
      --show-error \
      --connect-timeout 2 \
      --max-time 5 \
      --noproxy '*' \
      --header "@$mock_header_file" \
      --output "$response_file" \
      --write-out '%{http_code}' \
      "$mock_probe_url/v1/models"
  ); then
    fail "local OpenAI mock could not be reached at its advertised URL"
  fi
  if [ "$mock_status" != 200 ] || ! jq --exit-status --arg model "$model_name" '.data | map(.id) | index($model) != null' "$response_file" >/dev/null 2>&1; then
    fail "local OpenAI mock did not return its model contract"
  fi
}

delete_created_resources() {
  if [ "$token_created" = true ]; then
    http_request session DELETE "/api/token/$token_id" "$response_file"
    expect_http_status "200" "token cleanup"
    expect_api_success "$response_file" "token cleanup"
    token_created=false
    token_id=""
  fi

  if [ "$channel_created" = true ]; then
    http_request session DELETE "/api/channel/$channel_id" "$response_file"
    expect_http_status "200" "channel cleanup"
    expect_api_success "$response_file" "channel cleanup"
    channel_created=false
    channel_id=""
  fi
}

restore_performance_gate() {
  if [ "$performance_gate_changed" != true ]; then
    return
  fi
  local restore_payload
  restore_payload=$(jq --compact-output --null-input --argjson value "$performance_monitor_original" \
    '{key:"performance_setting.monitor_enabled",value:$value}')
  http_request session PUT /api/option/ "$response_file" "$restore_payload"
  expect_http_status "200" "performance gate restoration"
  expect_api_success "$response_file" "performance gate restoration"
  performance_gate_changed=false
}

log "waiting for $base_url"
wait_for_readiness

http_request public GET /api/status "$response_file"
expect_http_status "200" "/api/status"
expect_api_success "$response_file" "/api/status"
if ! jq --exit-status '.data.version | type == "string" and length > 0' "$response_file" >/dev/null 2>&1; then
  fail "/api/status did not expose a version"
fi
if [ -n "$expected_version" ] && ! jq --exit-status --arg version "$expected_version" '.data.version == $version' "$response_file" >/dev/null 2>&1; then
  fail "/api/status version does not match RUNTIME_SMOKE_EXPECTED_VERSION"
fi
log "status contract passed"

http_request public GET /api/readiness "$response_file"
expect_http_status "200" "/api/readiness"
expect_api_success "$response_file" "/api/readiness"
if ! jq --exit-status '.status == "ready"' "$response_file" >/dev/null 2>&1; then
  fail "/api/readiness did not return the ready contract"
fi
log "readiness contract passed"

http_request public GET /api/setup "$response_file"
expect_http_status "200" "/api/setup"
expect_api_success "$response_file" "/api/setup"
if ! jq --exit-status '.data.status | type == "boolean"' "$response_file" >/dev/null 2>&1; then
  fail "/api/setup did not expose a boolean initialization status"
fi
setup_complete=$(jq -r '.data.status' "$response_file")

if [ "$setup_complete" = false ]; then
  if [ "$base_is_loopback" != 1 ] && [ "$allow_remote_initialization" != 1 ]; then
    fail "initializing a remote target requires RUNTIME_SMOKE_ALLOW_REMOTE_INITIALIZATION=1"
  fi
  fresh_setup=true
  admin_username=${configured_admin_username:-smokeadmin}
  admin_password=${configured_admin_password:-$(python3 -c 'import secrets; print("S7" + secrets.token_hex(9))')}
  if [ -z "$admin_username" ] || [ "${#admin_username}" -gt 12 ]; then
    fail "fresh setup requires a non-empty root username no longer than 12 characters"
  fi
  if [ "${#admin_password}" -lt 8 ]; then
    fail "fresh setup requires a root password at least 8 characters long"
  fi
  setup_payload=$(
    printf '%s' "$admin_password" |
      jq --compact-output --raw-input --slurp \
        --arg username "$admin_username" \
        '{username:$username,password:.,confirmPassword:.,SelfUseModeEnabled:false,DemoSiteEnabled:false}'
  )
  http_request public POST /api/setup "$response_file" "$setup_payload"
  unset setup_payload
  expect_http_status "200" "/api/setup initialization"
  expect_api_success "$response_file" "/api/setup initialization"
  log "fresh initialization contract passed"
else
  if [ "$allow_existing" != 1 ]; then
    fail "target is already initialized; explicit existing-instance authorization is required"
  fi
  if [ "$confirm_exclusive_target" != 1 ]; then
    fail "initialized targets require RUNTIME_SMOKE_CONFIRM_EXCLUSIVE_TARGET=1 and no concurrent user traffic"
  fi
  admin_username=$configured_admin_username
  admin_password=$configured_admin_password
  if [ -z "$admin_username" ] || [ -z "$admin_password" ]; then
    fail "initialized targets require RUNTIME_SMOKE_USERNAME and RUNTIME_SMOKE_PASSWORD"
  fi
  log "exclusive initialized-target contract passed"
fi

http_request public GET /api/setup "$response_file"
expect_http_status "200" "/api/setup after initialization"
expect_api_success "$response_file" "/api/setup after initialization"
if ! jq --exit-status '.data.status == true' "$response_file" >/dev/null 2>&1; then
  fail "initialization did not become authoritative"
fi

login_payload=$(
  printf '%s' "$admin_password" |
    jq --compact-output --raw-input --slurp \
      --arg username "$admin_username" \
      '{username:$username,password:.}'
)
http_request public POST /api/user/login "$response_file" "$login_payload"
unset login_payload admin_password configured_admin_password
expect_http_status "200" "/api/user/login"
expect_api_success "$response_file" "/api/user/login"
if ! jq --exit-status '.data.require_2fa != true and (.data.id | type == "number") and .data.id > 0 and .data.role == 100' "$response_file" >/dev/null 2>&1; then
  fail "login must complete as a root session without a pending 2FA challenge"
fi
user_id=$(jq -r '.data.id' "$response_file")

http_request session GET /api/user/self "$response_file"
expect_http_status "200" "/api/user/self"
expect_api_success "$response_file" "/api/user/self"
if ! jq --exit-status --argjson id "$user_id" '.data.id == $id and .data.role == 100' "$response_file" >/dev/null 2>&1; then
  fail "login cookie was not accepted by the authenticated user contract"
fi
http_request session GET /api/status/test "$response_file"
expect_http_status "200" "/api/status/test"
expect_api_success "$response_file" "/api/status/test"
log "root login cookie and database health contracts passed"

http_request session GET /api/performance/stats "$response_file"
expect_http_status "200" "/api/performance/stats"
expect_api_success "$response_file" "/api/performance/stats"
if ! jq --exit-status '.data.config.monitor_enabled | type == "boolean"' "$response_file" >/dev/null 2>&1; then
  fail "performance stats did not expose the monitor gate contract"
fi
performance_monitor_original=$(jq -r '.data.config.monitor_enabled' "$response_file")
if [ "$performance_monitor_original" = true ] && { [ "$fresh_setup" = true ] || [ "$pause_performance_gate" = 1 ]; }; then
  # A release runner can legitimately share a nearly-full host. Temporarily
  # pausing this operational overload gate lets the smoke validate application
  # relay behavior instead of the host's disk watermark. The exact prior value
  # is restored on both success and failure. Existing instances require the
  # explicit opt-in above; fresh instances are disposable by definition.
  performance_gate_changed=true
  performance_payload='{"key":"performance_setting.monitor_enabled","value":false}'
  http_request session PUT /api/option/ "$response_file" "$performance_payload"
  expect_http_status "200" "temporary performance gate pause"
  expect_api_success "$response_file" "temporary performance gate pause"
  log "temporarily paused the host overload gate for isolated relay validation"
fi

start_mock
log "local OpenAI mock is ready"

channel_payload=$(jq --compact-output --null-input \
  --arg name "$channel_name" \
  --arg key "$upstream_key" \
  --arg base_url "$mock_base_url" \
  --arg model "$model_name" \
  '{
    mode:"single",
    channel:{
      type:1,
      key:$key,
      status:1,
      name:$name,
      weight:1000,
      base_url:$base_url,
      models:$model,
      group:"default",
      priority:2147483647,
      auto_ban:0,
      other:"",
      other_info:"{}",
      settings:"{}"
    }
  }')
http_request session POST /api/channel/ "$response_file" "$channel_payload"
unset channel_payload
expect_http_status "200" "channel creation"
expect_api_success "$response_file" "channel creation"
channel_created=true

http_request session GET "/api/channel/?p=1&page_size=100&id_sort=true" "$response_file"
expect_http_status "200" "channel lookup"
expect_api_success "$response_file" "channel lookup"
if ! jq --exit-status --arg name "$channel_name" '[.data.items[]? | select(.name == $name)] | length == 1' "$response_file" >/dev/null 2>&1; then
  fail "created channel could not be resolved uniquely"
fi
channel_id=$(jq -r --arg name "$channel_name" 'first(.data.items[] | select(.name == $name) | .id)' "$response_file")
if ! [[ "$channel_id" =~ ^[1-9][0-9]*$ ]]; then
  fail "created channel returned an invalid id"
fi

http_request session GET /api/user/models "$response_file"
expect_http_status "200" "/api/user/models"
expect_api_success "$response_file" "/api/user/models"
if ! jq --exit-status --arg model "$model_name" '.data | index($model) != null' "$response_file" >/dev/null 2>&1; then
  fail "created channel model was not visible to the root user"
fi

token_payload=$(jq --compact-output --null-input \
  --arg name "$token_name" \
  --arg model "$model_name" \
  --argjson quota "$token_quota" \
  '{
    name:$name,
    expired_time:-1,
    remain_quota:$quota,
    unlimited_quota:false,
    model_limits_enabled:true,
    model_limits:$model,
    allow_ips:"",
    group:"default",
    cross_group_retry:false
  }')
http_request session POST /api/token/ "$response_file" "$token_payload"
unset token_payload
expect_http_status "200" "token creation"
expect_api_success "$response_file" "token creation"
token_created=true

http_request session GET "/api/token/?p=1&page_size=100" "$response_file"
expect_http_status "200" "token lookup"
expect_api_success "$response_file" "token lookup"
if ! jq --exit-status --arg name "$token_name" '[.data.items[]? | select(.name == $name)] | length == 1' "$response_file" >/dev/null 2>&1; then
  fail "created token could not be resolved uniquely"
fi
token_id=$(jq -r --arg name "$token_name" 'first(.data.items[] | select(.name == $name) | .id)' "$response_file")
if ! [[ "$token_id" =~ ^[1-9][0-9]*$ ]]; then
  fail "created token returned an invalid id"
fi

http_request session POST "/api/token/$token_id/key" "$response_file"
expect_http_status "200" "token key retrieval"
expect_api_success "$response_file" "token key retrieval"
token_key=$(jq -r '.data.key // empty' "$response_file")
if ! [[ "$token_key" =~ ^[A-Za-z0-9]{48}$ ]]; then
  fail "token key retrieval returned an invalid key contract"
fi
# The channel-id suffix is understood only for admin-owned tokens and forces
# relay distribution to the mock channel. Existing real channels can never be
# selected by this smoke, even if they expose the same model.
printf 'Authorization: Bearer sk-%s-%s\n' "$token_key" "$channel_id" >"$token_header_file"
printf 'Authorization: Bearer sk-%s\n' "$token_key" >"$token_readonly_header_file"
unset token_key

http_request token GET /v1/models "$response_file"
expect_http_status "200" "/v1/models"
if ! jq --exit-status --arg model "$model_name" '.success == true and .object == "list" and (.data | map(.id) | index($model) != null)' "$response_file" >/dev/null 2>&1; then
  fail "/v1/models did not expose the token-limited mock model"
fi
log "channel, token, and model-list contracts passed"

http_request session GET /api/user/self "$response_file"
expect_http_status "200" "pre-relay user quota"
expect_api_success "$response_file" "pre-relay user quota"
user_quota_before=$(jq -r '.data.quota' "$response_file")
user_used_before=$(jq -r '.data.used_quota' "$response_file")

http_request token_readonly GET /api/usage/token/ "$response_file"
expect_http_status "200" "pre-relay token usage"
if ! jq --exit-status '.code == true and .data.total_used == 0 and .data.total_available > 0' "$response_file" >/dev/null 2>&1; then
  fail "new token did not start with a clean finite quota contract"
fi
token_used_before=$(jq -r '.data.total_used' "$response_file")
token_available_before=$(jq -r '.data.total_available' "$response_file")

nonstream_payload=$(jq --compact-output --null-input \
  --arg model "$model_name" \
  '{model:$model,messages:[{role:"user",content:"runtime smoke"}],temperature:0,max_tokens:16,stream:false}')
http_request token POST /v1/chat/completions "$response_file" "$nonstream_payload"
unset nonstream_payload
expect_http_status "200" "non-streaming relay"
if ! jq --exit-status --arg model "$model_name" '
  .model == $model and
  .choices[0].message.content == "runtime smoke ok" and
  .choices[0].finish_reason == "stop" and
  .usage.prompt_tokens == 5 and
  .usage.completion_tokens == 7 and
  .usage.total_tokens == 12
' "$response_file" >/dev/null 2>&1; then
  fail "non-streaming relay response did not match the local mock contract"
fi

stream_payload=$(jq --compact-output --null-input \
  --arg model "$model_name" \
  '{model:$model,messages:[{role:"user",content:"runtime smoke"}],temperature:0,max_tokens:16,stream:true,stream_options:{include_usage:true}}')
http_request token POST /v1/chat/completions "$stream_file" "$stream_payload"
unset stream_payload
expect_http_status "200" "streaming relay"
if ! grep -Fq 'runtime smoke stream' "$stream_file" ||
  ! grep -Fq '"usage":' "$stream_file" ||
  ! grep -Fq 'data: [DONE]' "$stream_file"; then
  fail "streaming relay response did not preserve content, usage, and DONE contracts"
fi
log "non-streaming and streaming relay contracts passed"

http_request token_readonly GET /api/usage/token/ "$response_file"
expect_http_status "200" "post-relay token usage"
if ! jq --exit-status '.code == true' "$response_file" >/dev/null 2>&1; then
  fail "post-relay token usage contract failed"
fi
token_used_after=$(jq -r '.data.total_used' "$response_file")
token_available_after=$(jq -r '.data.total_available' "$response_file")

http_request session GET /api/user/self "$response_file"
expect_http_status "200" "post-relay user quota"
expect_api_success "$response_file" "post-relay user quota"
user_quota_after=$(jq -r '.data.quota' "$response_file")
user_used_after=$(jq -r '.data.used_quota' "$response_file")

for numeric_value in \
  "$user_quota_before" "$user_used_before" "$user_quota_after" "$user_used_after" \
  "$token_used_before" "$token_available_before" "$token_used_after" "$token_available_after"; do
  if ! [[ "$numeric_value" =~ ^[0-9]+$ ]]; then
    fail "quota endpoints returned a non-integer accounting field"
  fi
done

token_charge=$((token_available_before - token_available_after))
token_used_delta=$((token_used_after - token_used_before))
user_charge=$((user_quota_before - user_quota_after))
user_used_delta=$((user_used_after - user_used_before))
if [ "$token_charge" -le 0 ] || [ "$token_charge" -ne "$token_used_delta" ]; then
  fail "token quota did not settle to one positive, balanced charge"
fi
if [ "$user_charge" -ne "$token_charge" ] || [ "$user_used_delta" -ne "$token_charge" ]; then
  fail "user and token quota accounting did not settle to the same charge"
fi

http_request session GET "/api/log/self?type=2&token_name=$token_name&model_name=$model_name&p=1&page_size=20" "$response_file"
expect_http_status "200" "consume log lookup"
expect_api_success "$response_file" "consume log lookup"
if ! jq --exit-status --arg token "$token_name" --arg model "$model_name" --argjson charged "$token_charge" '
  .data.total == 2 and
  (.data.items | length == 2) and
  ([.data.items[] | select(.token_name == $token and .model_name == $model and .quota > 0)] | length == 2) and
  ([.data.items[] | select(.is_stream == true)] | length == 1) and
  ([.data.items[] | select(.is_stream == false)] | length == 1) and
  ([.data.items[] | .quota] | add == $charged) and
  ([.data.items[] | select(.prompt_tokens == 5 and .completion_tokens == 7)] | length == 2)
' "$response_file" >/dev/null 2>&1; then
  fail "consume logs did not reconcile both relay modes with the charged quota"
fi
log "quota settlement and consume-log reconciliation passed"

# Payment checks below are deliberately read-only or invalid before provider
# selection. They cannot create a payable quote/order, call a gateway, debit a
# balance, or produce a real charge.
http_request session GET /api/user/topup/info "$response_file"
expect_http_status "200" "top-up information"
expect_api_success "$response_file" "top-up information"
if ! jq --exit-status '
  (.data.payment_routes | type == "array") and
  (.data.payment_products | type == "array") and
  (.data.payment_route_options | type == "array") and
  (.data.payment_compliance_confirmed | type == "boolean") and
  (.data.enable_redemption | type == "boolean") and
  (.data.online_payment_available | type == "boolean") and
  ([.data | .. | objects | keys[] |
    select(
      . == "provider" or
      . == "payment_provider" or
      . == "payment_method" or
      . == "credential_generation" or
      . == "webhook"
    )] | length == 0) and
  (.data | has("pay_methods") | not) and
  (.data | has("enable_online_topup") | not) and
  (.data | has("enable_stripe_topup") | not) and
  (.data | has("enable_xorpay_topup") | not) and
  (.data | has("xorpay_min_topup") | not)
' "$response_file" >/dev/null 2>&1; then
  fail "top-up information did not expose the safe payment capability contract"
fi
payment_compliance_confirmed=$(jq -r '.data.payment_compliance_confirmed' "$response_file")

http_request session GET "/api/user/topup/self?p=1&page_size=20" "$response_file"
expect_http_status "200" "top-up history before safe payment probes"
expect_api_success "$response_file" "top-up history before safe payment probes"
topup_total_before=$(jq -r '.data.total' "$response_file")

http_request session GET /api/subscription/plans "$response_file"
expect_http_status "200" "subscription plans"
expect_api_success "$response_file" "subscription plans"
if ! jq --exit-status '
  (.data | type == "array") and
  ([.data | .. | objects | keys[] |
    select(
      . == "stripe_price_id" or
      . == "creem_product_id" or
      . == "waffo_pancake_product_id" or
      . == "upgrade_group" or
      . == "downgrade_group" or
      . == "enabled" or
      . == "sort_order"
    )] | length == 0)
' "$response_file" >/dev/null 2>&1; then
  fail "subscription plans did not return the safe public contract"
fi

http_request session GET /api/subscription/self "$response_file"
expect_http_status "200" "subscription state before safe payment probes"
expect_api_success "$response_file" "subscription state before safe payment probes"
if ! jq --exit-status '
  (.data.subscriptions | type == "array") and
  (.data.all_subscriptions | type == "array") and
  ([.data | .. | objects | keys[] |
    select(
      . == "user_id" or
      . == "payment_order_id" or
      . == "source" or
      . == "upgrade_group" or
      . == "downgrade_group" or
      . == "prev_user_group" or
      . == "amount_used_total" or
      . == "usage_accounting_version" or
      . == "quota_reset_version" or
      . == "created_at" or
      . == "updated_at"
    )] | length == 0)
' "$response_file" >/dev/null 2>&1; then
  fail "subscription state did not return the safe public contract"
fi
subscription_total_before=$(jq -r '.data.all_subscriptions | length' "$response_file")

invalid_quote_payload='{"order_kind":"topup","route_id":"runtime-smoke-invalid","amount":0}'
http_request session POST /api/user/payment/quote "$response_file" "$invalid_quote_payload"
expect_http_status "400" "invalid top-up quote"
if ! jq --exit-status '
  .success == false and
  .code == "payment_method_unavailable" and
  (.data? == null)
' "$response_file" >/dev/null 2>&1; then
  fail "invalid top-up quote did not fail closed"
fi

invalid_start_payload=$(jq --compact-output --null-input --arg request_id "runtime-smoke-$run_id" \
  '{quote_id:"runtime-smoke-missing-quote",request_id:$request_id}')
http_request session POST /api/user/payment/start "$response_file" "$invalid_start_payload"
unset invalid_start_payload
expect_http_status "404" "missing payment quote start"
if ! jq --exit-status '
  .success == false and
  .code == "payment_quote_not_found" and
  (.data? == null)
' "$response_file" >/dev/null 2>&1; then
  fail "missing payment quote did not fail closed"
fi

http_request session GET "/api/user/payment/orders/runtime-smoke-missing-$run_id" "$response_file"
expect_http_status "404" "missing payment order lookup"
if ! jq --exit-status '
  .success == false and
  .code == "payment_order_not_found" and
  (.data? == null)
' "$response_file" >/dev/null 2>&1; then
  fail "missing payment order did not return the not-found contract"
fi

invalid_subscription_payload=$(jq --compact-output --null-input --arg request_id "runtime-smoke-$run_id" \
  '{plan_id:0,request_id:$request_id}')
http_request session POST /api/subscription/balance/pay "$response_file" "$invalid_subscription_payload"
unset invalid_subscription_payload
expect_http_status "200" "invalid subscription balance purchase"
if [ "$payment_compliance_confirmed" = true ]; then
  expected_subscription_error=payment_request_invalid
else
  expected_subscription_error=payment_temporarily_unavailable
fi
if ! jq --exit-status --arg expected_code "$expected_subscription_error" '
  .success == false and
  .code == $expected_code and
  (.data? == null)
' "$response_file" >/dev/null 2>&1; then
  fail "invalid subscription balance purchase did not fail closed"
fi
unset expected_subscription_error payment_compliance_confirmed

http_request session GET /api/user/self "$response_file"
expect_http_status "200" "quota after safe payment probes"
expect_api_success "$response_file" "quota after safe payment probes"
if ! jq --exit-status --argjson quota "$user_quota_after" --argjson used "$user_used_after" '
  .data.quota == $quota and
  .data.used_quota == $used and
  (.data | has("stripe_customer") | not)
' "$response_file" >/dev/null 2>&1; then
  fail "safe payment probes changed user quota"
fi

http_request token_readonly GET /api/usage/token/ "$response_file"
expect_http_status "200" "token quota after safe payment probes"
if ! jq --exit-status --argjson used "$token_used_after" --argjson available "$token_available_after" \
  '.code == true and .data.total_used == $used and .data.total_available == $available' "$response_file" >/dev/null 2>&1; then
  fail "safe payment probes changed token quota"
fi

http_request session GET "/api/user/topup/self?p=1&page_size=20" "$response_file"
expect_http_status "200" "top-up history after safe payment probes"
expect_api_success "$response_file" "top-up history after safe payment probes"
if ! jq --exit-status --argjson total "$topup_total_before" '.data.total == $total' "$response_file" >/dev/null 2>&1; then
  fail "safe payment probes created a top-up record"
fi

http_request session GET /api/subscription/self "$response_file"
expect_http_status "200" "subscription state after safe payment probes"
expect_api_success "$response_file" "subscription state after safe payment probes"
if ! jq --exit-status --argjson total "$subscription_total_before" '.data.all_subscriptions | length == $total' "$response_file" >/dev/null 2>&1; then
  fail "safe payment probes changed subscription state"
fi
log "recharge, payment, and subscription fail-closed contracts passed without a gateway call"

delete_created_resources
restore_performance_gate
if [[ "$mock_pid" =~ ^[1-9][0-9]*$ ]]; then
  kill "$mock_pid" >/dev/null 2>&1 || true
  wait "$mock_pid" >/dev/null 2>&1 || true
  mock_pid=""
fi

log "all runtime contracts passed; temporary channel and token were removed"
