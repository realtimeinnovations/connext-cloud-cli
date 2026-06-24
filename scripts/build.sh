#!/usr/bin/env sh

# Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
# No duplications, whole or partial, manual or electronic, may be made
# without express written permission.  Any such copies, or revisions thereof,
# must display this notice unaltered.
# This code contains trade secrets of Real-Time Innovations, Inc.


# Script for local builds that injects the Auth0 client ID from environment
# variables or a .env file.

set -eu

if { [ -z "${AUTH0_CLIENT_ID:-}" ] || [ -z "${WORKSPACES_AUTH0_CLIENT_ID:-}" ]; } && [ -f .env ]; then
  set -a
  . ./.env
  set +a
fi

output="${OUTPUT:-rticloud}"
client_id="${AUTH0_CLIENT_ID:-${CONNEXT_CLOUD_CLI_CLIENT_ID:-}}"
workspaces_client_id="${WORKSPACES_AUTH0_CLIENT_ID:-${CONNEXT_WORKSPACES_CLI_CLIENT_ID:-}}"
version="${VERSION:-dev}"
commit="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
date="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

if [ -z "$client_id" ] || [ -z "$workspaces_client_id" ]; then
  cat >&2 <<EOF
Missing required Auth0 client ID.

Set one Cloud client ID:
  AUTH0_CLIENT_ID
  CONNEXT_CLOUD_CLI_CLIENT_ID

Set one Workspaces client ID:
  WORKSPACES_AUTH0_CLIENT_ID
  CONNEXT_WORKSPACES_CLI_CLIENT_ID

For local development, create a gitignored .env file:
  cp .env.example .env
  # then edit .env
EOF
  exit 1
fi

go build \
  -ldflags "-X github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo.version=$version -X github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo.commit=$commit -X github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo.date=$date -X github.com/realtimeinnovations/connext-cloud-cli/config.defaultClientID=$client_id -X github.com/realtimeinnovations/connext-cloud-cli/config.defaultWorkspacesClientID=$workspaces_client_id" \
  -o "$output" ./cmd/rticloud
