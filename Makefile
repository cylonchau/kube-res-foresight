.DEFAULT_GOAL := help
GOCMD ?= go
BIN_DIR ?= target/bin
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0
VERSION := $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/cylonchau/kube-res-foresight/pkg/version.Version=$(VERSION)
# the compiled console of the frontend repository's releases (2026.11.03.1 style versions); empty WEB_VERSION means the latest
WEB_REPO ?= cylonchau/kube-res-foresight-web
WEB_VERSION ?=
IMAGE_REPO ?= kube-res-foresight
IMAGE_TAG ?= $(VERSION)
DOCKER ?= docker
.PHONY: docker docker-operator docker-release docker-operator-release all modules kube-res-foresight kube-res-foresight-release operator build build-web package-web fetch-web compile-platform test lint clean help
all: modules
modules: kube-res-foresight operator
build: modules
build-web:
	cd web && pnpm build
package-web: build-web
	mkdir -p pkg/app/router/dist
	rm -rf pkg/app/router/dist/*
	cp -r web/dist/. pkg/app/router/dist/
# download the compiled console from a release of the frontend repository instead of building it (no node or pnpm needed)
fetch-web:
	@set -e; 	version="$(WEB_VERSION)"; 	auth=""; if [ -n "$$GITHUB_TOKEN" ]; then auth="Authorization: Bearer $$GITHUB_TOKEN"; fi; 	if [ -z "$$version" ]; then 		version=$$(curl -fsSL -H "$$auth" https://api.github.com/repos/$(WEB_REPO)/releases/latest | grep -m1 '"tag_name"' | cut -d'"' -f4); 	fi; 	test -n "$$version" || { echo "no release of $(WEB_REPO) found"; exit 1; }; 	echo "console $$version"; 	mkdir -p target/web && curl -fsSL -H "$$auth" -o target/web/web.tar.gz 		https://github.com/$(WEB_REPO)/releases/download/$$version/kube-res-foresight-web_$$version.tar.gz; 	rm -rf pkg/app/router/dist && mkdir -p pkg/app/router/dist; 	tar -xzf target/web/web.tar.gz -C pkg/app/router/dist; 	test -s pkg/app/router/dist/index.html
# the platform: API, admin UI, metrics and the job engine in one binary
kube-res-foresight: package-web compile-platform
# the same platform built with a released console (make kube-res-foresight-release WEB_VERSION=2026.11.03.1)
kube-res-foresight-release: fetch-web compile-platform
compile-platform:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOCMD) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/kube-res-foresight ./cmd
# the operator that scales the workloads of the platform modules, a separate small binary with no web assets
operator:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOCMD) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/kube-res-foresight-operator ./cmd/operator
# images: the console is built in the image (node inside the build)
docker:
	$(DOCKER) build -f deploy/docker/Dockerfile --target kube-res-foresight --build-arg VERSION=$(VERSION) -t $(IMAGE_REPO):$(IMAGE_TAG) .
docker-operator:
	$(DOCKER) build -f deploy/docker/Dockerfile --target operator --build-arg VERSION=$(VERSION) -t $(IMAGE_REPO)-operator:$(IMAGE_TAG) .
# the platform image with the console of a frontend release, no node (make docker-release WEB_VERSION=2026.11.03.1)
docker-release: fetch-web
	$(DOCKER) build -f deploy/docker/Dockerfile --target kube-res-foresight --build-arg VERSION=$(VERSION) 		--build-arg CONSOLE_FROM=console --build-context console=pkg/app/router/dist -t $(IMAGE_REPO):$(IMAGE_TAG) .
test:
	$(GOCMD) test ./...
lint:
	golangci-lint run ./...
clean:
	rm -rf target coverage.out coverage.html
help:
	@echo "kube-res-foresight: make modules (kube-res-foresight, operator) | build-web | package-web | fetch-web | docker | docker-operator | docker-release | kube-res-foresight-release [WEB_VERSION=] | test | lint"
