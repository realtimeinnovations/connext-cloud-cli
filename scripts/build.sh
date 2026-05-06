#!/usr/bin/env sh

# Script for local builds that injects the Auth0 client ID from environment
# variables or a .env file.

set -eu

if [ -z "${AUTH0_CLIENT_ID:-}" ] && [ -z "${CONNEXT_CLOUD_CLI_CLIENT_ID:-}" ] && [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi

output="${OUTPUT:-rticloud}"
client_id="${AUTH0_CLIENT_ID:-${CONNEXT_CLOUD_CLI_CLIENT_ID:-}}"
version="${VERSION:-dev}"
commit="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
date="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

if [ -z "$client_id" ]; then
  cat >&2 <<EOF
Missing Auth0 client ID.

Set one of these:
  AUTH0_CLIENT_ID
  CONNEXT_CLOUD_CLI_CLIENT_ID

For local development, create a gitignored .env file:
  cp .env.example .env
  # then edit .env
EOF
  exit 1
fi

go build \
  -ldflags "-X github.com/realtimeinnovations/connext-cloud-cli/app.version=$version -X github.com/realtimeinnovations/connext-cloud-cli/app.commit=$commit -X github.com/realtimeinnovations/connext-cloud-cli/app.date=$date -X github.com/realtimeinnovations/connext-cloud-cli/config.defaultClientID=$client_id" \
  -o "$output" ./cmd/rticloud
