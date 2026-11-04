# syntax=docker/dockerfile:1.7
ARG NODE_VERSION=22
ARG GO_VERSION=1.27
FROM node:${NODE_VERSION}-alpine AS admin-builder
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@10.5.2 --activate
COPY web/ ./
RUN pnpm install --frozen-lockfile --ignore-scripts && pnpm build
# the console is built here
FROM scratch AS admin-dist
COPY --from=admin-builder /src/web/dist/ /
FROM golang:${GO_VERSION}-alpine AS go-base
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
# the operator is a separate binary with no console: its image never needs the frontend, built or downloaded
FROM go-base AS operator-builder
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cylonchau/kube-res-foresight/pkg/version.Version=${VERSION}" -o /out/kube-res-foresight-operator ./cmd/operator
FROM go-base AS builder
COPY --from=admin-dist / ./pkg/app/router/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cylonchau/kube-res-foresight/pkg/version.Version=${VERSION}" -o /out/kube-res-foresight ./cmd
# the console of a frontend release, copied to console-release/ by make docker-release (no node in the build)
FROM go-base AS builder-release
RUN mkdir -p pkg/app/router/dist && cp -r console-release/. pkg/app/router/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cylonchau/kube-res-foresight/pkg/version.Version=${VERSION}" -o /out/kube-res-foresight ./cmd
FROM alpine:3.22 AS runtime-base
RUN apk add --no-cache ca-certificates tzdata && addgroup -S -g 10001 kube-res-foresight && adduser -S -u 10001 -G kube-res-foresight kube-res-foresight
WORKDIR /app
# a numeric user: Kubernetes (runAsNonRoot) cannot verify a user given by name
USER 10001:10001
FROM runtime-base AS kube-res-foresight
COPY --from=builder /out/kube-res-foresight /usr/local/bin/kube-res-foresight
ENTRYPOINT ["kube-res-foresight"]
FROM runtime-base AS operator
COPY --from=operator-builder /out/kube-res-foresight-operator /usr/local/bin/kube-res-foresight-operator
ENTRYPOINT ["kube-res-foresight-operator"]
FROM runtime-base AS kube-res-foresight-release
COPY --from=builder-release /out/kube-res-foresight /usr/local/bin/kube-res-foresight
ENTRYPOINT ["kube-res-foresight"]
