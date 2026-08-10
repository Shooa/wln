.PHONY: build test install clean

build:
	go build -o bin/wln ./cmd/wln
	cp bin/wln bin/wlna

test:
	go test ./...

install:
	@install_dir="$${GOBIN:-$$(go env GOPATH)/bin}"; \
	mkdir -p "$$install_dir"; \
	go build -o "$$install_dir/wln" ./cmd/wln; \
	cp "$$install_dir/wln" "$$install_dir/wlna"

clean:
	go clean
