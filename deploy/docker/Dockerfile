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
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/cylonchau/kubeforecast/pkg/version.Version=${VERSION}" -o /out/kubeforecast ./cmd && \
 CGO_ENABLED=0 go build -o /out/kubeforecast-server ./cmd/server && \
 CGO_ENABLED=0 go build -o /out/kubeforecast-job ./cmd/job
FROM alpine:3.22 AS runtime-base
RUN apk add --no-cache ca-certificates tzdata && addgroup -S kubeforecast && adduser -S -G kubeforecast kubeforecast
WORKDIR /app
USER kubeforecast
FROM runtime-base AS kubeforecast
COPY --from=builder /out/kubeforecast /usr/local/bin/kubeforecast
ENTRYPOINT ["kubeforecast"]
FROM runtime-base AS server
COPY --from=builder /out/kubeforecast-server /usr/local/bin/kubeforecast-server
ENTRYPOINT ["kubeforecast-server"]
FROM runtime-base AS job
COPY --from=builder /out/kubeforecast-job /usr/local/bin/kubeforecast-job
ENTRYPOINT ["kubeforecast-job"]
