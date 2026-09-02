BIN ?= meshery-mcp-poc
MESHERY_URL ?= http://127.0.0.1:9081

.PHONY: build test test-integration meshery-server lint clean

build:
	go build -o $(BIN) .

test:
	go test ./... -race -cover

## Drive the compiled binary against a real Meshery Server. Needs one running;
## see docs/INTEGRATION.md, or run `make meshery-server` in another shell.
test-integration:
	MESHERY_URL=$(MESHERY_URL) go test -tags integration -race -timeout 300s .

## Build and run a Meshery Server from source. The published image is amd64
## only and dies under emulation on arm64 during content seeding; the source
## builds natively. MESHERY_SRC must point at a meshery/meshery checkout.
meshery-server:
	@test -n "$(MESHERY_SRC)" || (echo "set MESHERY_SRC to a meshery/meshery checkout" && exit 1)
	cd $(MESHERY_SRC)/server/cmd && go build -o /tmp/meshery-server . && \
	PORT=9081 PROVIDER=Local USE_GO_POLICY_ENGINE=true LOG_LEVEL=3 \
	APP_PATH=./apps.json KEYS_PATH=../../server/permissions/keys.csv \
	MESHSYNC_DEFAULT_DEPLOYMENT_MODE=operator /tmp/meshery-server

lint:
	gofmt -l . | grep -v '^$$' && exit 1 || true
	go vet ./...

clean:
	rm -f $(BIN)
