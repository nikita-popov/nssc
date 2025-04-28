APP_NAME = nssc
CMD_DIR = cmd/$(APP_NAME)
GO = go

.PHONY: all build run clean deps

all: build

build:
	$(GO) get -d -v ./...
	$(GO) build -o bin/$(APP_NAME) $(CMD_DIR)/main.go

check:
	$(GO) test -v -cover ./...

run: build
	./bin/$(APP_NAME)

clean:
	rm -rf bin/$(APP_NAME)

deps:
	$(GO) mod tidy

adduser:
	@echo "Usage: make adduser ROOT=path USER=username PASS=password QUOTA=1GiB"
	$(GO) run $(CMD_DIR)/main.go adduser $(ROOT) $(USER) $(PASS) $(QUOTA)

# make server HOST=127.0.0.1:8080 ROOT=/srv/nssc/
server: build
	./bin/$(APP_NAME) run $(HOST) $(ROOT)
