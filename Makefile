.PHONY: build test test-integration lint clean

BINARY := tmux-harness

build:
	go build -o bin/$(BINARY) .
	go build -o bin/client ./client

test:
	go test -race -short ./...

test-integration:
	HARNESS_INTEGRATION=1 go test -race -tags integration ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/
