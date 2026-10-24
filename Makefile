TEST_DATABASE_URL ?= postgres://fogplay:fogplay@localhost:5432/fogplay?sslmode=disable
TEST_REDIS_URL ?= redis://localhost:6379/1

.PHONY: build run test deps up down

build:
	go build -o bin/fogplay ./cmd/fogplay

# Starts Postgres and Redis only, for running the API or tests from the host.
deps:
	docker compose up -d postgres redis

up:
	docker compose up --build

down:
	docker compose down

test:
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) TEST_REDIS_URL=$(TEST_REDIS_URL) go test -p 1 -race ./...
