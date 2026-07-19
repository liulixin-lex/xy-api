#!/usr/bin/env bash

set -Eeuo pipefail

for command_name in bun jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "required command is not installed: $command_name" >&2
    exit 1
  fi
done

audit_status=0
audit_json=$(NO_COLOR=1 bun audit --json --audit-level=moderate) || audit_status=$?

if ! jq --exit-status 'type == "object"' <<<"$audit_json" >/dev/null; then
  echo 'bun audit did not return the expected JSON object' >&2
  exit 1
fi

blocking_count=$(jq '[
  to_entries[].value[]? |
  select(.severity == "moderate" or .severity == "high" or .severity == "critical")
] | length' <<<"$audit_json")
if [ "$blocking_count" -ne 0 ]; then
  jq --raw-output '
    to_entries[] as $package |
    $package.value[]? |
    select(.severity == "moderate" or .severity == "high" or .severity == "critical") |
    "\(.severity): \($package.key): \(.title) (\(.url))"
  ' <<<"$audit_json" >&2
  echo "bun audit found $blocking_count moderate-or-higher vulnerability record(s)" >&2
  exit 1
fi

if [ "$audit_status" -ne 0 ]; then
  echo "bun audit failed with status $audit_status without returning a blocking advisory" >&2
  exit 1
fi

echo 'bun audit passed with no moderate, high, or critical vulnerabilities'
