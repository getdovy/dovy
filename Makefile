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

.PHONY: dovy test
