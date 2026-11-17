BINARY  := webeep-sync-cli
VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

.PHONY: build test vet fmt clean

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) .

test:
	go test -race ./...

vet:
	go vet ./...
	test -z "$$(gofmt -l .)"

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY)
