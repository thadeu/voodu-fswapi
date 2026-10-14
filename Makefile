BIN      := bin/voodu-fswapi
PKG      := ./cmd/voodu-fswapi
DIST     := dist
VERSION  := $(shell grep '^version:' plugin.yml | awk '{print $$2}')

GO       := go
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: build test lint vet cross clean install-local

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

# cross produces the two release binaries we ship.
cross: $(DIST)/voodu-fswapi_linux_amd64 $(DIST)/voodu-fswapi_linux_arm64

$(DIST)/voodu-fswapi_linux_amd64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -ldflags '$(LDFLAGS)' -o $@ $(PKG)

$(DIST)/voodu-fswapi_linux_arm64:
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -ldflags '$(LDFLAGS)' -o $@ $(PKG)

install-local: build
	@if [ -z "$(PLUGINS_ROOT)" ]; then \
		echo "PLUGINS_ROOT is required (e.g. /opt/voodu/plugins)"; exit 1; \
	fi
	@mkdir -p $(PLUGINS_ROOT)/fswapi/bin
	cp $(BIN) $(PLUGINS_ROOT)/fswapi/bin/voodu-fswapi
	cp plugin.yml $(PLUGINS_ROOT)/fswapi/
	cp install uninstall $(PLUGINS_ROOT)/fswapi/ 2>/dev/null || true
	@echo "installed into $(PLUGINS_ROOT)/fswapi"

clean:
	rm -rf $(BIN) $(DIST)
