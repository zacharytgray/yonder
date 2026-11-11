VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/zacharytgray/yonder/internal/version.Version=$(VERSION)

.PHONY: build test lint integration clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/yonder ./cmd/yonder

test:
	go test -race ./...

lint:
	go vet ./...
	go vet -tags integration ./...
	golangci-lint run

# needs docker; skips cleanly without it
integration:
	go test -tags integration -race -count=1 -v -run TestFleetOverSSH ./test/

clean:
	rm -rf bin dist
