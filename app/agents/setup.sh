#!/bin/bash
# Setup agent skill symlinks for local development.
# In production (k8s), these are baked into the Docker image via Dockerfile.
#
# Usage: ./app/agents/setup.sh

set -e

SKILL_DIR="$(cd "$(dirname "$0")/skills" && pwd)"

echo "Setting up agent skills from: $SKILL_DIR"

# Claude Code — skills are directories containing SKILL.md
mkdir -p ~/.claude/skills
for skill in "$SKILL_DIR"/*/; do
  name=$(basename "$skill")
  ln -sfn "$skill" ~/.claude/skills/"$name"
  echo "  Claude Code: ~/.claude/skills/$name -> $skill"
done

# OpenCode — commands are .md files
mkdir -p ~/.config/opencode/commands
for skill in "$SKILL_DIR"/*/SKILL.md; do
  name=$(basename "$(dirname "$skill")")
  ln -sf "$skill" ~/.config/opencode/commands/"$name.md"
  echo "  OpenCode:    ~/.config/opencode/commands/$name.md -> $skill"
done

echo "Done. Skills are linked for all agents."
