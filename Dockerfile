# ---- Build stage ----
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X main.version=${VERSION}" -o bot ./cmd/bot

# ---- Runtime stage ----
FROM node:22-alpine

RUN apk add --no-cache git ca-certificates curl

# Install agent CLIs globally
RUN npm install -g @anthropic-ai/claude-code @openai/codex

# Install opencode CLI (musl build for Alpine)
ARG OPENCODE_VERSION=1.4.3
RUN curl -sL https://github.com/anomalyco/opencode/releases/download/v${OPENCODE_VERSION}/opencode-linux-x64-musl.tar.gz | \
    tar xzf - -C /usr/local/bin opencode

# Install gh CLI (not available in Alpine apk)
ARG GH_VERSION=2.65.0
RUN curl -sL https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_amd64.tar.gz | \
    tar xz -C /usr/local/bin --strip-components=2 gh_${GH_VERSION}_linux_amd64/bin/gh

# Go binary
COPY --from=builder /app/bot /bot

# Agent skills
COPY agents/skills/ /opt/agents/skills/
RUN mkdir -p /home/node/.claude/skills && \
    for d in /opt/agents/skills/*/; do \
      ln -s "$d" /home/node/.claude/skills/$(basename "$d"); \
    done

# Repo cache directory (writable by node user)
RUN mkdir -p /data/repos && chown node:node /data/repos

# Config (default, can be overridden via ConfigMap volume mount)
COPY config.example.yaml /config.yaml

USER node
ENTRYPOINT ["/bot"]
CMD ["-config", "/config.yaml"]
