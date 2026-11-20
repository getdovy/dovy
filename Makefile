VERSION ?= 0.0.0-dev
LDFLAGS := -s -w -X main.version=$(VERSION)
ifdef CATALOG
LDFLAGS += -X github.com/getdovy/dovy/internal/catalog.URL=$(CATALOG)
endif

dovy:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dovy ./cmd/dovy

test:
	gofmt -l . | (! grep .)
	go vet ./...
	go test ./...

# deb builds in Debian 13 with its Go; the source is mounted read-only.
deb:
	mkdir -p dist
	docker run --rm -v "$(CURDIR)":/src:ro -v "$(CURDIR)/dist":/out debian:13 sh -ec '\
		apt-get update -qq && apt-get install -qq -y --no-install-recommends golang-go ca-certificates >/dev/null; \
		cp -r /src /tmp/w && cd /tmp/w && GOFLAGS=-buildvcs=false packaging/build-deb.sh $(VERSION) /out; \
		chown -R $(shell id -u):$(shell id -g) /out'

.PHONY: dovy test deb
