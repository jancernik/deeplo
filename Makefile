BIN     := bin/deeplo
VERSION := $(shell (git describe --tags --abbrev=0 2>/dev/null || echo "dev") | sed 's/^v//')
LDFLAGS := -s -w -X github.com/jancernik/deeplo/internal/build.Version=$(VERSION)

PLATFORMS := linux/amd64 linux/arm64

GOTESTSUM_FORMAT ?= testname
ifeq ($(shell command -v gotestsum 2>/dev/null),)
GOTEST := go test
else
GOTEST := gotestsum --format $(GOTESTSUM_FORMAT) --
endif

.PHONY: build test check test-unit test-integration test-all vet fmt lint clean release

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/deeplo

test: test-unit

check: fmt vet test-unit

test-unit:
	$(GOTEST) ./...

test-integration:
	$(GOTEST) -tags integration -race -timeout 5m \
		./internal/compose/ ./internal/engine/

test-all:
	$(GOTEST) -tags integration -race -timeout 5m ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

release:
	@mkdir -p bin
	@rm -f bin/deeplo_*
	@for platform in $(PLATFORMS); do \
		os=$$(echo $$platform | cut -d/ -f1); \
		arch=$$(echo $$platform | cut -d/ -f2); \
		echo "Building $$os/$$arch..."; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags="$(LDFLAGS)" \
			-o bin/deeplo_$${os}_$${arch} \
			./cmd/deeplo; \
	done

clean:
	rm -rf bin
