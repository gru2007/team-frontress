# Team Frontress coordinator

GO ?= go

.PHONY: build test race vet fmt run example clean

build:
	$(GO) build -o bin/coordinator ./cmd/coordinator

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=2 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# A local coordinator on the example settings. Dev auth, one static server.
run:
	$(GO) run ./cmd/coordinator -config coordinator.example.json -log-level debug

# Regenerate the annotated default config.
example:
	$(GO) run ./cmd/coordinator -print-config

clean:
	rm -rf bin
