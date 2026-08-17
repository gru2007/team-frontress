# GREYLINE FRONTRESS coordinator

GO      ?= go
PROTOC  ?= protoc
MODULE  := github.com/greyline-frontress/coordinator

.PHONY: build coordinator agent test race run fmt vet proto clean

build: coordinator agent

coordinator:
	$(GO) build -o bin/coordinator ./cmd/coordinator

# The agent runs next to a dedicated game server and joins it to the pool.
agent:
	$(GO) build -o bin/greyline-agent ./cmd/greyline-agent

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=2 ./...

# A local coordinator on the testbed settings. The war it starts is written to
# war-events.jsonl; delete that file to start a new one.
run:
	$(GO) run ./cmd/coordinator -config gc.test.json -log-level debug

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

# The retired peer-to-peer link still shares this schema with the game.
proto:
	$(PROTOC) --proto_path=proto --go_out=. --go_opt=module=$(MODULE) proto/greyline.proto

clean:
	rm -rf bin
