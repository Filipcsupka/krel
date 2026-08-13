.PHONY: build install test clean

build:
	go build ./cmd/kr
	go build ./cmd/krel

install:
	./scripts/install.sh

test:
	go test ./...

clean:
	go clean
	rm -f ./kr ./krel

