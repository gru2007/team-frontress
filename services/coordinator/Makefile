# GREYLINE FRONTRESS coordinator

GO      ?= go
PROTOC  ?= protoc
MODULE  := github.com/greyline-frontress/coordinator

.PHONY: build test race run proto fmt vet clean

build:
	$(GO) build -o bin/coordinator ./cmd/coordinator

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=2 ./...

# Requires GREYLINE_GC_SECRET in the environment.
run:
	$(GO) run ./cmd/coordinator -world world.example.json -log-level debug

proto:
	$(PROTOC) --proto_path=proto --go_out=. --go_opt=module=$(MODULE) proto/greyline.proto

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf bin
