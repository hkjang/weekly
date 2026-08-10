.PHONY: build test run image offline clean

VERSION ?= $(shell cat VERSION)
IMAGE ?= weekly:v$(VERSION)

build:
	./scripts/build.sh

test:
	go test ./...
	cd frontend && npm run lint

run: build
	./dist/weekly

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$$(git rev-parse --short HEAD 2>/dev/null || echo unknown) --build-arg BUILD_TIME=$$(date -u +%Y-%m-%dT%H:%M:%SZ) -t $(IMAGE) .

offline: image
	./scripts/export-offline.sh $(IMAGE)

clean:
	rm -rf dist release frontend/dist cmd/weekly/web/assets
