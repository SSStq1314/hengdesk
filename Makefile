.PHONY: build test lint web-install web-build web-lint up down
GO_DIR := server
build:
	cd $(GO_DIR) && CGO_ENABLED=0 go build -o ../bin/hengdesk ./cmd/hengdesk
test:
	cd $(GO_DIR) && go test -race ./...
lint:
	cd $(GO_DIR) && golangci-lint run ./...
web-install:
	cd web && pnpm install --frozen-lockfile
web-build:
	cd web && pnpm build
web-lint:
	cd web && pnpm lint && pnpm exec tsc -b
up:
	cd deploy && docker compose up -d --build
down:
	cd deploy && docker compose down
