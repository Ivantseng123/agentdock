#!/bin/bash
cd "$(dirname "$0")"

# Setup agent skills (idempotent, uses symlinks)
./agents/setup.sh

echo "Building..."
go build -o bot ./cmd/bot/ || exit 1
echo "Starting react2issue..."
exec ./bot -config config.yaml
