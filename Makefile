GO ?= go
PREFIX ?= /usr/local
REMOTE ?= origin
export VERSION REMOTE

.PHONY: build test check install release

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/agentiscode ./cmd/agentiscode

test:
	$(GO) test -race ./...

check:
	$(GO) vet ./...
	$(GO) test -race ./...

install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 bin/agentiscode $(DESTDIR)$(PREFIX)/bin/agentiscode

# Usage: make release VERSION=1.2.3 [REMOTE=origin]
release:
	@set -eu; \
	version="$${VERSION#v}"; \
	if ! printf '%s\n' "$$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$$'; then \
		printf '%s\n' 'Usage: make release VERSION=1.2.3 (or 1.2.3-rc.1)' >&2; exit 1; \
	fi; \
	tag="v$$version"; \
	git rev-parse --verify HEAD >/dev/null; \
	if [ -n "$$(git status --porcelain)" ]; then \
		printf '%s\n' 'Commit or stash changes before creating a release tag.' >&2; exit 1; \
	fi; \
	git remote get-url "$$REMOTE" >/dev/null; \
	git tag -a "$$tag" -m "Release $$tag"; \
	git push "$$REMOTE" "refs/tags/$$tag"
