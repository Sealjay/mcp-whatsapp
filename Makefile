.PHONY: build test test-race test-cover lint vet fmt clean login serve bridge smoke e2e upgrade-check

BIN := ./bin/whatsapp-mcp
PKGS := ./...
GO := go

build:
	mkdir -p bin
	$(GO) build -o $(BIN) ./cmd/whatsapp-mcp

test:
	$(GO) test $(PKGS)

test-race:
	$(GO) test -race $(PKGS)

test-cover:
	$(GO) test -race -coverprofile=cover.out $(PKGS)
	$(GO) tool cover -func=cover.out | tail -1

vet:
	$(GO) vet $(PKGS)

fmt:
	@diff -u <(echo -n) <(gofmt -d .) || (echo "gofmt differences found"; exit 1)

lint: vet fmt

e2e: build
	$(GO) test -tags=e2e ./e2e/...

login: build
	$(BIN) login

serve: build
	$(BIN) serve

bridge: build
	$(BIN) bridge

smoke: build
	$(BIN) smoke

clean:
	rm -rf bin cover.out

upgrade-check:
	$(GO) get go.mau.fi/whatsmeow@main
	$(GO) mod tidy
	$(GO) build ./...
	$(GO) test $(PKGS)
