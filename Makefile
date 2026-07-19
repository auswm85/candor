.PHONY: all build test vet lint tidy clean migrate dev

BINARY = candor

all: build

build:
	go build -o dist/$(BINARY) ./cmd/candor

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
	go run ./cmd/candor migrate

dev: build
	./dist/$(BINARY)
