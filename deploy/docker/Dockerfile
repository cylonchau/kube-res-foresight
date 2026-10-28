# syntax=docker/dockerfile:1.7
ARG NODE_VERSION=22
ARG GO_VERSION=1.27
FROM node:${NODE_VERSION}-alpine AS admin-builder
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@10.5.2 --activate
COPY web/ ./
RUN pnpm install --frozen-lockfile --ignore-scripts && pnpm build
FROM golang:${GO_VERSION}-alpine AS builder
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=admin-builder /src/web/dist/ ./pkg/app/router/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cylonchau/kube-resource-fcst/pkg/version.Version=${VERSION}" -o /out/kube-resource-fcst ./cmd && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cylonchau/kube-resource-fcst/pkg/version.Version=${VERSION}" -o /out/kube-resource-fcst-operator ./cmd/operator
FROM alpine:3.22 AS runtime-base
RUN apk add --no-cache ca-certificates tzdata && addgroup -S -g 10001 kube-resource-fcst && adduser -S -u 10001 -G kube-resource-fcst kube-resource-fcst
WORKDIR /app
# a numeric user: Kubernetes (runAsNonRoot) cannot verify a user given by name
USER 10001:10001
FROM runtime-base AS kube-resource-fcst
COPY --from=builder /out/kube-resource-fcst /usr/local/bin/kube-resource-fcst
ENTRYPOINT ["kube-resource-fcst"]
FROM runtime-base AS operator
COPY --from=builder /out/kube-resource-fcst-operator /usr/local/bin/kube-resource-fcst-operator
ENTRYPOINT ["kube-resource-fcst-operator"]
