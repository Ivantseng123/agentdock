#!/bin/bash
cd /Users/ivantseng/local_file/slack-issue-bot
echo "Building..."
go build -o bot ./cmd/bot/ || exit 1
echo "Starting slack-issue-bot..."
exec ./bot -config config.yaml
