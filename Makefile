# Building gocc and gocc-cli.
#
#   make            both binaries, into bin/
#   make gocc       just the desktop app
#   make gocc-cli   just the command line version
#   make install    both, into $(GOBIN) or $(GOPATH)/bin
#   make test       the suite, with the race detector
#   make dist       both, for every platform, into dist/
#   make clean      remove what this made
#
# The version comes from a git tag, or from the commit when there is none,
# and is baked into the binaries with -ldflags.

BIN      := bin
DIST     := dist
PACKAGE  := github.com/free/gocc
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   := $(shell git rev-parse --short HEAD 2>/dev/null)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X $(PACKAGE)/internal/version.Version=$(VERSION) \
            -X $(PACKAGE)/internal/version.Commit=$(COMMIT) \
            -X $(PACKAGE)/internal/version.Date=$(DATE)

# The platforms `make dist` builds for, as GOOS/GOARCH.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

GOBIN ?= $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN := $(shell go env GOPATH)/bin
endif

.PHONY: all gocc gocc-cli build install test test-race vet fmt lint dist clean help

all: gocc gocc-cli

# The desktop app, whose window MyGo draws. It needs GTK 3 to run on
# Linux, but not to build: the toolkit is reached without cgo.
gocc: $(BIN)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/gocc .

# The command line version, which serves the same thing to a browser.
gocc-cli: $(BIN)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/gocc-cli ./cmd/gocc-cli

$(BIN):
	mkdir -p $(BIN)

# Install both into the Go binary directory, which is on PATH for most.
install:
	go install -trimpath -ldflags "$(LDFLAGS)" .
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/gocc-cli

test:
	go test ./...

# The suite with the race detector, which is what the interfaces and the
# engine are tested under: both are used from several goroutines.
test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Both binaries for every platform in PLATFORMS, into dist/. The version
# is in each name, so that a directory of builds says what each one is.
dist:
	@mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		goos=$${p%/*}; goarch=$${p#*/}; \
		ext=""; [ "$$goos" = "windows" ] && ext=".exe"; \
		for cmd in gocc gocc-cli; do \
			dir=.; [ "$$cmd" = "gocc-cli" ] && dir=./cmd/gocc-cli; \
			out=$(DIST)/$$cmd-$(VERSION)-$$goos-$$goarch$$ext; \
			echo "  $$out"; \
			GOOS=$$goos GOARCH=$$goarch CGO_ENABLED=0 \
				go build -trimpath -ldflags "$(LDFLAGS)" -o "$$out" $$dir; \
		done; \
	done
	@echo "built $(VERSION) for: $(PLATFORMS)"

clean:
	rm -rf $(BIN) $(DIST)

help:
	@sed -n '2,15p' $(MAKEFILE_LIST) | sed 's/^# \{0,1\}//'
