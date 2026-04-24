#!/bin/bash
cd "$(dirname "$0")"

# Setup agent skills (idempotent, uses symlinks)
./app/agents/setup.sh

echo "Building..."
go build -o agentdock ./cmd/agentdock/ || exit 1
echo "Starting react2issue..."
exec ./agentdock app -c config.yaml
