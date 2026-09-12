# Mailman -- a localhost mail catcher you can reply from.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# CGO_ENABLED=0 is what makes cross-compiling possible at all: the SQLite
# driver is pure Go, so there is no C toolchain to arrange per target.
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

# Where `make install` puts the binary. The default needs no sudo, which
# matters because install also registers a login item and asking for root to
# do that would be the wrong shape entirely.
PREFIX ?= $(HOME)/.local

.PHONY: all build ui dev dev-ui test lint clean dist install install-ui checksums

all: build

## build: compile the binary with the current UI baked in
build: ui
	@mkdir -p build
	go build -trimpath -ldflags "$(LDFLAGS)" -o build/mailman ./cmd/mailman

## ui: compile the React app into web/dist, which the binary embeds
ui:
	cd web && npm run build

install-ui:
	cd web && npm install

## install: build, put mailman on PATH and register it to run at login
install: build
	install -d $(PREFIX)/bin
	install -m 0755 build/mailman $(PREFIX)/bin/mailman
	$(PREFIX)/bin/mailman service install

## dev: run the server against the source, without rebuilding the UI
dev:
	go run ./cmd/mailman

## dev-ui: the Vite dev server, proxying the API to a running `make dev`
dev-ui:
	cd web && npm run dev

## test: the Go suite plus the frontend's type check
#
# -race matches what CI runs. Without it the settings reload, which swaps a
# configuration snapshot other request goroutines are reading, could pass
# locally and fail there.
test:
	go test -race ./...
	cd web && npm run types:check

lint:
	go vet ./...
	gofmt -l cmd internal web

clean:
	rm -rf build

## dist: cross-compile every published platform
dist: ui
	@mkdir -p build/dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=build/dist/mailman-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "  $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $$out ./cmd/mailman || exit 1; \
	done

## checksums: what the install script verifies a download against
checksums: dist
	cd build/dist && shasum -a 256 mailman-* > SHA256SUMS
