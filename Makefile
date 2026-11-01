VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PREFIX  ?= $(HOME)/.local
LDFLAGS  = -s -w -X main.version=$(VERSION)
GOFLAGS_STATIC = CGO_ENABLED=0

.PHONY: build test vet fmt lint check install uninstall release clean fuzz

build: ## build ./ak820 (static, no dependencies)
	$(GOFLAGS_STATIC) go build -trimpath -ldflags "$(LDFLAGS)" -o ak820 ./cmd/ak820

test: ## all tests with the race detector
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt: ## fail if any file needs gofmt
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

LINT_VERSION ?= latest
lint: ## golangci-lint, fetched on demand (not a module dependency)
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION) run ./...

check: fmt vet test ## tests and vet; CI also runs lint

fuzz: ## 10 seconds on each fuzz target
	go test ./internal/hidraw -run XXX -fuzz ^FuzzParseDescriptor$$ -fuzztime 10s
	go test ./internal/keyboard/legacy -run XXX -fuzz ^FuzzDecode$$ -fuzztime 10s
	go test ./internal/animation -run XXX -fuzz ^FuzzDecode$$ -fuzztime 10s

install: build ## install to $(PREFIX)/bin (default ~/.local/bin, no sudo needed)
	install -Dm755 ak820 $(PREFIX)/bin/ak820
	@echo "installed $(PREFIX)/bin/ak820"; case ":$$PATH:" in *":$(PREFIX)/bin:"*) ;; *) echo "note: $(PREFIX)/bin is not on your PATH yet; add it, or run $(PREFIX)/bin/ak820";; esac

uninstall:
	rm -f $(PREFIX)/bin/ak820

release: ## dist/ak820_<version>_linux_{amd64,arm64}.tar.gz
	@rm -rf dist && mkdir -p dist
	@for arch in amd64 arm64; do \
	  d=dist/ak820_$(VERSION)_linux_$$arch; mkdir -p $$d/udev; \
	  GOOS=linux GOARCH=$$arch $(GOFLAGS_STATIC) go build -trimpath -ldflags "$(LDFLAGS)" -o $$d/ak820 ./cmd/ak820 || exit 1; \
	  cp LICENSE README.md $$d/; cp udev/70-ak820.rules $$d/udev/; \
	  tar -C dist -czf $$d.tar.gz ak820_$(VERSION)_linux_$$arch && rm -rf $$d; \
	  echo "built $$d.tar.gz"; \
	done

clean:
	rm -rf ak820 dist
