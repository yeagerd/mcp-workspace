.PHONY: build test test-integration lint clean

BINARY := hangar

build:
	go build -o bin/$(BINARY) .
	go build -o bin/cli ./client

test:
	go test -race -short ./...

test-integration:
	HANGAR_INTEGRATION=1 go test -race -tags integration ./...

lint:
	golangci-lint run

clean:
	rm -rf bin/
