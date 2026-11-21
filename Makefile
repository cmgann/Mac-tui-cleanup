GO ?= go
VERSION ?= 0.2.0
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build release test vet
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean ./cmd/devclean
release:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean-darwin-arm64 ./cmd/devclean
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean-darwin-amd64 ./cmd/devclean
test:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
