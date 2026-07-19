.PHONY: all build build-daemon build-cli test vet lint tidy clean migrate web-build web-dev dev

BINARY_DAEMON = candor
BINARY_CLI    = tt

all: build

build: build-daemon build-cli

build-daemon:
	go build -o dist/$(BINARY_DAEMON) ./cmd/candor

build-cli:
	go build -o dist/$(BINARY_CLI) ./cmd/tt

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

lint:
	golangci-lint run

tidy:
	go mod tidy

clean:
	rm -rf dist/

migrate:
	go run ./cmd/tt migrate

web-build:
	cd web && npm ci && npm run build

web-dev:
	cd web && npm run dev

dev: build-cli
	./dist/$(BINARY_CLI) daemon