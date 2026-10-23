.DEFAULT_GOAL := help
GOCMD ?= go
BIN_DIR ?= target/bin
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0
VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/cylonchau/kubeforecast/pkg/version.Version=$(VERSION)
.PHONY: all modules kubeforecast server job build build-web package-web test lint clean help
all: modules
modules: kubeforecast server job
build: modules
build-web:
	cd web && pnpm build
package-web: build-web
	mkdir -p pkg/app/router/dist
	rm -rf pkg/app/router/dist/*
	cp -r web/dist/. pkg/app/router/dist/
kubeforecast: package-web
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOCMD) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/kubeforecast ./cmd
server: package-web
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOCMD) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/kubeforecast-server ./cmd/server
job:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOCMD) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/kubeforecast-job ./cmd/job
test:
	$(GOCMD) test ./...
lint:
	golangci-lint run ./...
clean:
	rm -rf target coverage.out coverage.html
help:
	@echo "KubeForecast: make modules | build-web | package-web | test | lint"
