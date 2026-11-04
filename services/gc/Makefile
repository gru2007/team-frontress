# Team Frontress game coordinator

GO ?= go

.PHONY: build test race vet fmt run proto clean

build:
	$(GO) build -o bin/frontress-gc ./cmd/frontress-gc

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=2 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

# A local GC on the example settings: dev auth, state in ./data.
run:
	$(GO) run ./cmd/frontress-gc -config gc.example.json -debug

# Regenerate internal/gcpb from the game's .proto files.
proto:
	./proto/gen.sh

clean:
	rm -rf bin data
