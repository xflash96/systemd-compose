# Targets for working on systemd-compose. `make check` is what CI's unit job
# runs; `make live` runs the end-to-end tests in systemd containers (needs
# docker).

GO ?= go
PREFIX ?= $(HOME)/.local

.PHONY: build install check test race lint e2e live snapshot clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o systemd-compose .

# The static binary in PREFIX/bin, and the man page in PREFIX/share/man/man1.
install:
	mkdir -p $(PREFIX)/bin $(PREFIX)/share/man/man1
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags='-s -w' -o $(PREFIX)/bin/systemd-compose .
	cp docs/systemd-compose.1 $(PREFIX)/share/man/man1/

check:
	ci/unit

test:
	$(GO) test -count=1 ./...

race:
	$(GO) test -race -count=1 ./...

lint:
	$(GO) vet ./...
	$(GO) run honnef.co/go/tools/cmd/staticcheck@v0.6.1 ./...

# The end-to-end tests, on your own user manager, with throwaway projects.
e2e:
	$(GO) test -tags e2e -count=1 -v -timeout 30m ./e2e/

# The end-to-end tests in containers that boot systemd 249 and 255.
live:
	ci/container 22.04
	ci/container 24.04

# The release archives, as CI builds them, into dist/ (needs goreleaser).
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf systemd-compose dist third_party
