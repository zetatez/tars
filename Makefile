BINARY    := tars
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X tars/internal/api.Version=$(VERSION)
BUILD_DIR := build

.PHONY: all build test vet fmt fmt-check lint run install clean docker config

all: build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILD_DIR)/$(BINARY) ./cmd/tars

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

lint: vet fmt-check

run:
	go run ./cmd/tars --config /opt/tars/config.yaml

install: build
	install -Dm755 $(BUILD_DIR)/$(BINARY) /opt/tars/bin/$(BINARY)
	@test -f /opt/tars/config.yaml || (install -Dm644 config.example.yaml /opt/tars/config.yaml && echo "created /opt/tars/config.yaml")
	@mkdir -p /opt/tars/data /opt/tars/work
	@ln -sf /opt/tars/bin/$(BINARY) /usr/local/bin/$(BINARY)
	@echo "installed: /opt/tars/bin/$(BINARY)  ->  /usr/local/bin/$(BINARY)"
	@echo "config: /opt/tars/config.yaml  data: /opt/tars/data"

clean:
	rm -rf $(BUILD_DIR)

docker:
	docker build -t $(BINARY):$(VERSION) .

config:
	@test -f /opt/tars/config.yaml || (install -Dm644 config.example.yaml /opt/tars/config.yaml && echo "created /opt/tars/config.yaml")
