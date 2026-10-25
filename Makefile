.DEFAULT_GOAL := help
GOCMD ?= go
BIN_DIR ?= target/bin
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0
VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/cylonchau/kube-resource-fcst/pkg/version.Version=$(VERSION)
.PHONY: all modules kube-resource-fcst operator build build-web package-web test lint clean help
all: modules
modules: kube-resource-fcst operator
build: modules
build-web:
	cd web && pnpm build
package-web: build-web
	mkdir -p pkg/app/router/dist
	rm -rf pkg/app/router/dist/*
	cp -r web/dist/. pkg/app/router/dist/
# the platform: API, admin UI, metrics and the job engine in one binary
kube-resource-fcst: package-web
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOCMD) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/kube-resource-fcst ./cmd
# the PredictiveScaler controller, a separate small binary with no web assets
operator:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOCMD) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/kube-resource-fcst-operator ./cmd/operator
test:
	$(GOCMD) test ./...
lint:
	golangci-lint run ./...
clean:
	rm -rf target coverage.out coverage.html
help:
	@echo "kube-resource-fcst: make modules (kube-resource-fcst, operator) | build-web | package-web | test | lint"
