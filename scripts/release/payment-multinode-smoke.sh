#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: PAYMENT_MULTINODE_SQL_DSN=... PAYMENT_MULTINODE_SPLIT_SQL_DSN=... \
  PAYMENT_MULTINODE_REDIS_URL=... \
  payment-multinode-smoke.sh <new-api-binary>

Runs a real multi-process payment-readiness smoke against one shared
PostgreSQL/MySQL database and one shared Redis instance. No payment provider is
called and no merchant credential is required.

Both supplied databases must be dedicated to this smoke because the
application will run its normal additive migrations in them. The split DSN is
used to prove that an expected member attached to the wrong database remains
fail-closed.
EOF
}

if [ "$#" -ne 1 ]; then
  usage
  exit 2
fi

binary=$1
sql_dsn=${PAYMENT_MULTINODE_SQL_DSN:-}
split_sql_dsn=${PAYMENT_MULTINODE_SPLIT_SQL_DSN:-}
redis_url=${PAYMENT_MULTINODE_REDIS_URL:-}
base_port=${PAYMENT_MULTINODE_BASE_PORT:-39100}

if [ ! -x "$binary" ]; then
  echo "payment multi-node smoke failed: binary is not executable: $binary" >&2
  exit 1
fi
if [ -z "$sql_dsn" ]; then
  echo 'payment multi-node smoke failed: PAYMENT_MULTINODE_SQL_DSN is required' >&2
  exit 1
fi
if [ -z "$split_sql_dsn" ]; then
  echo 'payment multi-node smoke failed: PAYMENT_MULTINODE_SPLIT_SQL_DSN is required' >&2
  exit 1
fi
if [ -z "$redis_url" ]; then
  echo 'payment multi-node smoke failed: PAYMENT_MULTINODE_REDIS_URL is required' >&2
  exit 1
fi
if ! [[ "$base_port" =~ ^[0-9]+$ ]] || [ "$base_port" -lt 1024 ] || [ "$base_port" -gt 65525 ]; then
  echo 'payment multi-node smoke failed: PAYMENT_MULTINODE_BASE_PORT must be between 1024 and 65525' >&2
  exit 1
fi

for command_name in curl jq mktemp; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "payment multi-node smoke failed: required command is not installed: $command_name" >&2
    exit 1
  fi
done

runtime_directory=$(mktemp -d)
declare -A active_pids=()

cleanup() {
  local pid
  for pid in "${!active_pids[@]}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill -TERM "$pid" >/dev/null 2>&1 || true
    fi
  done
  for pid in "${!active_pids[@]}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      wait "$pid" >/dev/null 2>&1 || true
    fi
  done
  find "$runtime_directory" -depth -delete
}
trap cleanup EXIT INT TERM

shared_session_secret='payment-multinode-session-secret-not-for-production-0001'
shared_crypto_secret='payment-multinode-crypto-secret-not-for-production-000001'
shared_payment_secret='payment-multinode-payment-secret-not-for-production-00001'

start_node() {
  local label=$1
  local node_name=$2
  local port=$3
  local session_secret=$4
  local node_sql_dsn=${5:-$sql_dsn}
  local node_directory="$runtime_directory/$label"
  local log_file="$runtime_directory/$label.log"
  mkdir -p "$node_directory/logs"
  (
    cd "$node_directory"
    env \
      GIN_MODE=release \
      NODE_NAME="$node_name" \
      SESSION_SECRET="$session_secret" \
      CRYPTO_SECRET="$shared_crypto_secret" \
      PAYMENT_SECRET_KEY="$shared_payment_secret" \
      PAYMENT_MULTI_NODE_ENABLED=true \
      PAYMENT_CLUSTER_ID=payment-runtime-smoke \
      PAYMENT_CLUSTER_NODES=payment-smoke-a,payment-smoke-b,payment-smoke-c \
      PAYMENT_CLUSTER_MIN_LIVE_NODES=2 \
      SQL_DSN="$node_sql_dsn" \
      REDIS_CONN_STRING="$redis_url" \
      SYNC_FREQUENCY=1 \
      PORT="$port" \
      "$binary" --port "$port" --log-dir "$node_directory/logs"
  ) >"$log_file" 2>&1 &
  STARTED_PID=$!
  active_pids["$STARTED_PID"]=1
}

stop_node() {
  local pid=$1
  if ! kill -0 "$pid" >/dev/null 2>&1; then
    wait "$pid" >/dev/null 2>&1 || true
    unset 'active_pids[$pid]'
    return
  fi
  kill -TERM "$pid"
  for _ in $(seq 1 100); do
    if ! kill -0 "$pid" >/dev/null 2>&1; then
      wait "$pid" >/dev/null 2>&1 || true
      unset 'active_pids[$pid]'
      return
    fi
    sleep 0.1
  done
  echo "payment multi-node smoke failed: process $pid did not stop cleanly" >&2
  return 1
}

wait_for_status() {
  local port=$1
  local expected_status=$2
  local description=$3
  local status
  # PostgreSQL 9.6 catalog introspection can make the first rolling migration
  # noticeably slower on constrained CI runners. Keep polling bounded, but do
  # not turn a healthy second replica into a timing-dependent failure.
  for _ in $(seq 1 720); do
    status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
      --max-time 2 "http://127.0.0.1:${port}/api/readiness" 2>/dev/null || true)
    if [ "$status" = "$expected_status" ]; then
      return
    fi
    sleep 0.25
  done
  echo "payment multi-node smoke failed: $description expected HTTP $expected_status, got ${status:-none}" >&2
  for log_file in "$runtime_directory"/*.log; do
    if [ -f "$log_file" ]; then
      echo "--- $(basename "$log_file") ---" >&2
      tail -n 80 "$log_file" >&2
    fi
  done
  return 1
}

port_a=$base_port
port_b=$((base_port + 1))
port_c=$((base_port + 2))
port_mismatch=$((base_port + 3))
port_duplicate=$((base_port + 4))
port_split=$((base_port + 5))

start_node node-a payment-smoke-a "$port_a" "$shared_session_secret"
pid_a=$STARTED_PID
wait_for_status "$port_a" 503 'single node fail-closed behavior while an expected peer is missing'

start_node node-b payment-smoke-b "$port_b" "$shared_session_secret"
pid_b=$STARTED_PID
wait_for_status "$port_a" 200 'first node readiness after adding a matching peer'
wait_for_status "$port_b" 200 'second node readiness with matching shared configuration'

response_file="$runtime_directory/response.json"
cookie_jar="$runtime_directory/session.cookies"
touch "$response_file" "$cookie_jar"
chmod 600 "$response_file" "$cookie_jar"
setup_status=$(curl --silent --show-error --output "$response_file" --write-out '%{http_code}' \
  --max-time 5 "http://127.0.0.1:${port_a}/api/setup")
if [ "$setup_status" != 200 ] || ! jq --exit-status '.success == true and .data.status == false' "$response_file" >/dev/null; then
  echo 'payment multi-node smoke failed: dedicated database was not a fresh setup target' >&2
  exit 1
fi
setup_payload='{"username":"clusteradmin","password":"ClusterSmoke9!","confirmPassword":"ClusterSmoke9!","SelfUseModeEnabled":false,"DemoSiteEnabled":false}'
setup_status=$(curl --silent --show-error --output "$response_file" --write-out '%{http_code}' \
  --max-time 10 --header 'Content-Type: application/json' --request POST \
  --data "$setup_payload" "http://127.0.0.1:${port_a}/api/setup")
if [ "$setup_status" != 200 ] || ! jq --exit-status '.success == true' "$response_file" >/dev/null; then
  echo 'payment multi-node smoke failed: shared database setup failed' >&2
  exit 1
fi
wait_for_status "$port_a" 200 'first node readiness after shared configuration mutation'
wait_for_status "$port_b" 200 'second node readiness after shared configuration synchronization'

start_node node-c payment-smoke-c "$port_c" "$shared_session_secret"
pid_c=$STARTED_PID
wait_for_status "$port_a" 200 'first node readiness after adding a third expected peer'
wait_for_status "$port_b" 200 'second node readiness after adding a third expected peer'
wait_for_status "$port_c" 200 'third node readiness with shared configuration'

login_payload='{"username":"clusteradmin","password":"ClusterSmoke9!"}'
login_status=$(curl --silent --show-error --output "$response_file" --write-out '%{http_code}' \
  --max-time 10 --header 'Content-Type: application/json' --request POST \
  --cookie "$cookie_jar" --cookie-jar "$cookie_jar" --data "$login_payload" \
  "http://127.0.0.1:${port_a}/api/user/login")
if [ "$login_status" != 200 ] || ! jq --exit-status \
  '.success == true and .data.require_2fa != true and .data.role == 100 and (.data.id | type == "number")' \
  "$response_file" >/dev/null; then
  echo 'payment multi-node smoke failed: login through the first node failed' >&2
  exit 1
fi
logged_in_user_id=$(jq --raw-output '.data.id' "$response_file")
session_status=$(curl --silent --show-error --output "$response_file" --write-out '%{http_code}' \
  --max-time 10 --header "New-Api-User: $logged_in_user_id" \
  --cookie "$cookie_jar" --cookie-jar "$cookie_jar" \
  "http://127.0.0.1:${port_b}/api/user/self")
if [ "$session_status" != 200 ] || ! jq --exit-status --argjson id "$logged_in_user_id" \
  '.success == true and .data.id == $id and .data.role == 100' "$response_file" >/dev/null; then
  echo 'payment multi-node smoke failed: the second node rejected the shared login session' >&2
  jq --sort-keys . "$response_file" >&2 || cat "$response_file" >&2
  exit 1
fi

stop_node "$pid_b"
wait_for_status "$port_a" 200 'first node remains ready while one expected member restarts'
wait_for_status "$port_c" 200 'third node remains ready while one expected member restarts'

start_node node-key-mismatch payment-smoke-b "$port_mismatch" \
  'payment-multinode-different-session-secret-not-for-production'
pid_mismatch=$STARTED_PID
wait_for_status "$port_a" 503 'first node fail-closed behavior for a peer key mismatch'
wait_for_status "$port_c" 503 'third node fail-closed behavior for a peer key mismatch'
wait_for_status "$port_mismatch" 503 'mismatched peer fail-closed behavior'
stop_node "$pid_mismatch"
wait_for_status "$port_a" 200 'first node recovery after mismatched peer unregisters'
wait_for_status "$port_c" 200 'third node recovery after mismatched peer unregisters'

start_node node-b-recovered payment-smoke-b "$port_b" "$shared_session_secret"
pid_b=$STARTED_PID
wait_for_status "$port_a" 200 'first node readiness after the expected peer returns'
wait_for_status "$port_b" 200 'second node readiness after returning with matching configuration'
wait_for_status "$port_c" 200 'third node readiness after the expected peer returns'

stop_node "$pid_b"
wait_for_status "$port_a" 200 'first node remains ready before split-database probe'
wait_for_status "$port_c" 200 'third node remains ready before split-database probe'
start_node node-split-database payment-smoke-b "$port_split" "$shared_session_secret" "$split_sql_dsn"
pid_split=$STARTED_PID
wait_for_status "$port_split" 503 'expected member attached to the wrong database fails closed'
wait_for_status "$port_a" 503 'first node detects the split database membership view'
wait_for_status "$port_c" 503 'third node detects the split database membership view'
stop_node "$pid_split"
wait_for_status "$port_a" 200 'first node recovers after split-database member unregisters'
wait_for_status "$port_c" 200 'third node recovers after split-database member unregisters'

start_node node-b-final payment-smoke-b "$port_b" "$shared_session_secret"
pid_b=$STARTED_PID
wait_for_status "$port_a" 200 'first node readiness after split-database member is corrected'
wait_for_status "$port_b" 200 'corrected second node readiness'
wait_for_status "$port_c" 200 'third node readiness after split-database member is corrected'

start_node node-duplicate payment-smoke-a "$port_duplicate" "$shared_session_secret"
pid_duplicate=$STARTED_PID
wait_for_status "$port_a" 503 'first node fail-closed behavior for duplicate node identity'
wait_for_status "$port_b" 503 'second node fail-closed behavior for duplicate node identity'
wait_for_status "$port_c" 503 'third node fail-closed behavior for duplicate node identity'
wait_for_status "$port_duplicate" 503 'duplicate node fail-closed behavior'
stop_node "$pid_duplicate"
wait_for_status "$port_a" 200 'first node recovery after duplicate identity unregisters'
wait_for_status "$port_b" 200 'second node recovery after duplicate identity unregisters'
wait_for_status "$port_c" 200 'third node recovery after duplicate identity unregisters'

stop_node "$pid_c"
stop_node "$pid_b"
stop_node "$pid_a"

echo 'payment multi-node runtime smoke passed'
