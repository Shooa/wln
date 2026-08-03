.PHONY: build test install clean

build:
	go build -o bin/wln ./cmd/wln

test:
	go test ./...

install:
	go install ./cmd/wln

clean:
	go clean

