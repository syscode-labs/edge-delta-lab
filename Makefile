.PHONY: build test race vet check demo docker-smoke container container-multi compose-prepare compose-up release-binaries
GO ?= go
PYTHON ?= python3
HELM ?= helm
VERSION ?=
SOURCE_DATE_EPOCH ?= 0
SIZE_MIB ?= 16
RATE_KBIT ?= 5000

build:
	$(GO) build -trimpath -o bin/edgelab ./cmd/edgelab
	$(GO) build -trimpath -o bin/edgelab-exporter ./cmd/edgelab-exporter

test:
	$(GO) test -count=1 ./...
	$(PYTHON) -m unittest discover -s scripts -p '*_test.py' -v

race:
	$(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

check: vet test race

demo: build
	$(PYTHON) scripts/demo.py --size-mib $(SIZE_MIB) --rate-kbit $(RATE_KBIT)

docker-smoke: build
	$(PYTHON) scripts/docker_smoke.py --size-mib $(SIZE_MIB) --rate-kbit $(RATE_KBIT)

container: build
	mkdir -p bin/container
	CGO_ENABLED=0 GOOS=linux GOARCH=$$(docker version --format '{{.Server.Arch}}') $(GO) build -trimpath -o bin/container/edgelab ./cmd/edgelab
	docker build -t edge-delta-lab:local .

# Multi-stage build straight from source (dev + hub and client targets).
# daemon target: sender/hub with the admin socket under /state.
# client target: dry-run-by-default client image, no exposed port.
container-multi:
	docker build --target daemon -t edge-delta-lab:local .
	docker build --target client -t edge-delta-lab:client .

compose-prepare: build
	$(PYTHON) scripts/prepare_compose.py

compose-up: container
	docker compose up

.PHONY: release
release: export VERSION := $(VERSION)
release: export SOURCE_DATE_EPOCH := $(SOURCE_DATE_EPOCH)
release: export GO := $(GO)
release: export HELM := $(HELM)
release:
	$(PYTHON) scripts/release.py

# Cross-compiled release binaries (static, path-trimmed, size-stripped).
# Matrix covers linux/amd64, linux/arm64 and darwin/arm64.
release-binaries:
	mkdir -p bin/release
	for pair in linux/amd64 linux/arm64 darwin/arm64; do \
		os=$${pair%/*}; arch=$${pair#*/}; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "-s -w" -o bin/release/edgelab-$$os-$$arch ./cmd/edgelab || exit 1; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "-s -w" -o bin/release/edgelab-exporter-$$os-$$arch ./cmd/edgelab-exporter || exit 1; \
	done
	ls -l bin/release

# Main demonstration: real Docker images, not synthetic archives.
.PHONY: docker-demo transport-demo sender-status sender-collect diagrams openspec-check
WORK ?= work/docker-demo
ORIGIN ?= http://127.0.0.1:8080
DB ?= $(WORK)/sender/collector.sqlite

docker-demo: build
	$(PYTHON) scripts/docker_demo.py --work $(WORK) --size-mib $(SIZE_MIB) --rate-kbit $(RATE_KBIT)

transport-demo: demo

sender-status:
	$(PYTHON) scripts/sender.py status --db $(DB) --watch

sender-collect:
	$(PYTHON) scripts/sender.py collect --origin $(ORIGIN) --db $(DB)

diagrams:
	$(PYTHON) scripts/diagrams.py

openspec-check:
	$(PYTHON) scripts/check_specs.py
