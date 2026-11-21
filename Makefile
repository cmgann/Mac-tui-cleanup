GO ?= go
VERSION ?= 0.4.0
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build release test vet
build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean ./cmd/devclean
release:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean-darwin-arm64 ./cmd/devclean
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean-darwin-amd64 ./cmd/devclean
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean-linux-amd64 ./cmd/devclean
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/devclean-linux-arm64 ./cmd/devclean
test:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
