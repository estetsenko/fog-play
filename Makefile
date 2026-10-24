TEST_DATABASE_URL ?= postgres://fogplay:fogplay@localhost:5432/fogplay?sslmode=disable
TEST_REDIS_URL ?= redis://localhost:6379/1

.PHONY: build test deps up down dist

build:
	go build -o bin/ ./cmd/...

VERSION ?= dev
PLATFORMS := windows/amd64 darwin/arm64 darwin/amd64 linux/amd64

# The fogplay client zipped per platform, plus the server for Linux.
dist:
	rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		out=dist/fogplay-$(VERSION)-$$os-$$arch; mkdir -p $$out; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="-s -w" -o $$out/fogplay$$ext ./cmd/fogplay || exit 1; \
		(cd dist && zip -qr fogplay-$(VERSION)-$$os-$$arch.zip fogplay-$(VERSION)-$$os-$$arch) && rm -rf $$out; \
	done
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/fogplay-server-$(VERSION)-linux-amd64 ./cmd/fogplay-server

# Starts Postgres and Redis only, for running the API or tests from the host.
deps:
	docker compose up -d postgres redis

up:
	docker compose up --build

down:
	docker compose down

test:
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) TEST_REDIS_URL=$(TEST_REDIS_URL) go test -p 1 -race ./...
