FROM golang:1.22-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o bot ./cmd/bot

FROM alpine:3.19
RUN apk add --no-cache git ca-certificates
COPY --from=builder /app/bot /bot
ENTRYPOINT ["/bot"]
