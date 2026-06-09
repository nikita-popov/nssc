REPO := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))

GO = go

VERSION := $(shell printf '%s-dev' "$$(git describe --tags --always --dirty 2>/dev/null || echo unknown)")
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build nssc clean

all: deps build

build: nssc

deps:
	$(GO) mod tidy
	$(GO) mod download

get:
	$(GO) get -v ./...

nssc:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/nssc cmd/nssc/main.go

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test -v -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out
	$(GO) tool cover -html=coverage.out -o coverage.html

check: fmt vet test

clean:
	$(GO) clean -v
	rm -rf $(REPO)/bin

adduser:
	@echo "Usage: make adduser ROOT=path USER=username PASS=password QUOTA=1GiB"
	$(GO) run $(CMD_DIR)/main.go adduser $(ROOT) $(USER) $(PASS) $(QUOTA)

# make server HOST=127.0.0.1:8080 ROOT=/srv/nssc/
server: build
	./bin/$(APP_NAME) run $(HOST) $(ROOT)
