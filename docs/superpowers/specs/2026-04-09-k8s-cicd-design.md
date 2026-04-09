# K8s Deployment & CI/CD Design

## Overview

Add Kubernetes deployment manifests (Kustomize) and Jenkins CI/CD pipelines for the react2issue bot. The bot runs as a single pod with Socket Mode (no ingress needed), spawns `claude` CLI as a subprocess for codebase analysis.

## Dockerfile

Multi-stage build: Go binary + Node.js runtime (for claude CLI) + gh CLI.

```dockerfile
# ---- Build stage ----
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bot ./cmd/bot

# ---- Runtime stage ----
FROM node:22-alpine

RUN apk add --no-cache git ca-certificates curl

# Install claude CLI globally
RUN npm install -g @anthropic-ai/claude-code

# Install gh CLI (not available in Alpine apk — download from GitHub releases)
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
```

### .dockerignore

```
.git/
docs/
*.md
config.yaml
config.local.yaml
bot
.idea/
.vscode/
deploy/overlays/*/
!deploy/overlays/example/
```

Key decisions:
- Base image: `node:22-alpine` (claude CLI requires Node.js)
- `gh` CLI downloaded from GitHub releases (not available in Alpine apk)
- Skills symlinked at build time (same pattern as local dev)
- Runs as `node` user (non-root, UID 1000)
- `/data/repos` created with correct ownership for repo cache
- Auth tokens injected via env vars at runtime (not baked in)

## Kustomize Structure

```
deploy/
  base/                              # In repo, no sensitive info
    kustomization.yaml
    deployment.yaml
  overlays/
    example/                         # In repo, placeholder values
      kustomization.yaml.example
      configmap.yaml.example
      secret.yaml.example
    <env>/                           # Gitignored, actual deploy config
      kustomization.yaml
      configmap.yaml
      secret.yaml
```

### Base Layer

**kustomization.yaml:**
```yaml
labels:
  - includeSelectors: true
    pairs:
      app: react2issue
resources:
  - deployment.yaml
```

**deployment.yaml:**
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: react2issue
spec:
  replicas: 1
  strategy:
    type: Recreate    # Single replica + Socket Mode — avoid duplicate event processing
  template:
    spec:
      imagePullSecrets:
        - name: regcred
      containers:
        - name: react2issue
          image: IMAGE_PLACEHOLDER
          imagePullPolicy: Always
          ports:
            - containerPort: 8080
              name: http
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 5
            timeoutSeconds: 5
            failureThreshold: 5
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 3
            timeoutSeconds: 5
          resources:
            limits:
              cpu: "2"
              memory: 4Gi
            requests:
              cpu: 100m
              memory: 512Mi
          envFrom:
            - secretRef:
                name: react2issue-secret
          volumeMounts:
            - name: repo-cache
              mountPath: /data/repos
      volumes:
        - name: repo-cache
          emptyDir:
            sizeLimit: 10Gi
  revisionHistoryLimit: 3
```

Notes:
- `strategy: Recreate` — single replica Socket Mode bot must not have two pods running simultaneously (independent dedup maps → duplicate issues)
- `memory: 4Gi` — Go binary (~50MB) + claude CLI Node.js process (~500MB-1GB) + git clone working data. With `max_concurrent: 3`, multiple agent processes can run simultaneously
- `containerPort: 8080` — matches `config.example.yaml` `server.port: 8080`
- `emptyDir` for repo cache — ephemeral, re-clones on pod restart. Acceptable trade-off: avoids PVC complexity, repos are re-fetched on demand (cached in memory after first fetch)
- `revisionHistoryLimit: 3` — allows `kubectl rollout undo`
- No Service needed — Socket Mode is outbound WebSocket, health check is handled by kubelet probes directly

### Overlay Example

**kustomization.yaml.example:**
```yaml
labels:
  - includeSelectors: true
    pairs:
      app: react2issue
namespace: your-namespace
resources:
  - ../../base
  - configmap.yaml
  - secret.yaml
images:
  - name: IMAGE_PLACEHOLDER
    newName: your-registry.example.com/your-org/react2issue
    newTag: "0.1.0"
```

**configmap.yaml.example:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: react2issue-config
data:
  LOG_LEVEL: "info"
```

**secret.yaml.example:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: react2issue-secret
type: Opaque
stringData:
  GH_TOKEN: "ghp_your-token"
  SLACK_BOT_TOKEN: "xoxb-your-token"
  SLACK_APP_TOKEN: "xapp-your-token"
  CLAUDE_AUTH_TOKEN: "your-claude-setup-token"
```

### Gitignore

```
deploy/overlays/*/
!deploy/overlays/example/
```

### Key Differences from Reference Project (jasmine-policy)

- No ingress (Socket Mode, no public URL needed)
- No Service (outbound WebSocket only)
- No startupProbe / liveness-patch (Go binary starts instantly)
- `strategy: Recreate` instead of `RollingUpdate` (single replica Socket Mode)
- All tokens in Secret (not ConfigMap)
- `emptyDir` for repo cache (no PVC)
- Higher memory limit (4Gi — claude CLI + concurrent agents)
- Port 8080 (matches config.example.yaml)

## Jenkins CI/CD

### Jenkinsfile-bump-version

Triggered manually with parameters: `version` (semver), `ref` (target branch).

Flow:
1. Setup: git auth + gh auth
2. Check Tag: validate semver, update kustomization.yaml.example newTag
3. Unit Testing: `go test ./...`
4. Bump New Version: create PR, auto-merge

Agent pod: `golang:1.25-alpine` + `git` containers.

```groovy
pipeline {
  agent {
    kubernetes {
      cloud 'SLKE'
      defaultContainer 'golang'
      yaml """
kind: Pod
spec:
  securityContext:
    runAsUser: 0
  containers:
  - name: golang
    image: golang:1.25-alpine
    command: ['cat']
    tty: true
    resources:
      limits:
        memory: "2Gi"
        cpu: "1"
  - name: git
    image: your-registry.example.com/library/git:2
    command: ['cat']
    tty: true
    resources:
      limits:
        memory: "100Mi"
        cpu: "100m"
"""
    }
  }

  environment {
    CREDENTIAL = credentials("your-credential-id")
  }

  stages {
    stage('Setup') { /* git auth + gh auth */ }
    stage('Check Tag') { /* semver validation, update newTag */ }
    stage('Unit Testing') { steps { sh "go test ./..." } }
    stage('Bump New Version') { /* PR create + auto-merge */ }
  }
}
```

### Jenkinsfile-release

Triggered manually with parameters: `tag` (semver), `ref` (target branch).

Flow:
1. Setup: git auth + gh auth
2. Preflight Checks: validate semver, check tag doesn't exist
3. Build & Push Image: `docker build` + `docker push` to registry
4. Publish GitHub Release: `gh release create` with auto-generated notes

Agent pod: `golang:1.25-alpine` + `git` + `docker:24` containers. Docker socket mounted for image build.

```groovy
pipeline {
  agent {
    kubernetes {
      cloud 'SLKE'
      defaultContainer 'golang'
      yaml """
kind: Pod
spec:
  securityContext:
    runAsUser: 0
  containers:
  - name: golang
    image: golang:1.25-alpine
    command: ['cat']
    tty: true
    resources:
      limits:
        memory: "2Gi"
        cpu: "1"
  - name: git
    image: your-registry.example.com/library/git:2
    command: ['cat']
    tty: true
    resources:
      limits:
        memory: "100Mi"
        cpu: "100m"
  - name: docker
    image: docker:24
    command: ['cat']
    tty: true
    volumeMounts:
    - name: dockersock
      mountPath: /var/run/docker.sock
  volumes:
  - name: dockersock
    hostPath:
      path: /var/run/docker.sock
"""
    }
  }

  environment {
    CREDENTIAL = credentials("your-credential-id")
    REGISTRY = "your-registry.example.com"
    IMAGE_NAME = "your-org/react2issue"
  }

  stages {
    stage('Setup') { /* git auth + gh auth */ }
    stage('Preflight Checks') { /* semver + tag existence check */ }
    stage('Build & Push Image') {
      steps {
        container('docker') {
          sh """
          docker build -t ${REGISTRY}/${IMAGE_NAME}:${tag} .
          docker push ${REGISTRY}/${IMAGE_NAME}:${tag}
          """
        }
      }
    }
    stage('Publish GitHub Release') {
      steps {
        container('git') {
          sh "gh release create ${tag} --target ${ref} --generate-notes"
        }
      }
    }
  }
}
```

### Key Differences from Reference Project

- `golang` container replaces `maven` (no m2 PVC needed)
- `docker` container added for image build/push
- No redoc-cli, curl-jq (no OpenAPI)
- No artifact repo deploy
- Registry and image name via Jenkins env vars (not hardcoded)
- Credential ID uses placeholder

## Config Override Strategy

The bot reads `config.yaml` at startup. In k8s:

1. `config.example.yaml` is baked into the image as `/config.yaml`
2. Overlay can mount a ConfigMap as a volume to replace `/config.yaml`
3. Sensitive env vars (`SLACK_BOT_TOKEN`, `GH_TOKEN`, `CLAUDE_AUTH_TOKEN`) are injected via Secret
4. The bot's `config.Load()` reads the file; env vars are available to the claude CLI subprocess

## Claude CLI Authentication in K8s

1. Locally: `claude setup-token` to generate a long-lived token
2. Store token as k8s Secret (`CLAUDE_AUTH_TOKEN`)
3. Pod env var injects the token → claude CLI authenticates with Max plan
4. No interactive login needed in the pod
