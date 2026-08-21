# GREYLINE FRONTRESS coordinator

GO      ?= go
PROTOC  ?= protoc
MODULE  := github.com/greyline-frontress/coordinator

.PHONY: build coordinator agent ban test race check check-maps check-seams game-test run fmt vet proto clean

build: coordinator agent ban

coordinator:
	$(GO) build -o bin/coordinator ./cmd/coordinator

# The agent runs next to a dedicated game server and joins it to the pool.
agent:
	$(GO) build -o bin/greyline-agent ./cmd/greyline-agent

# The operator's ban console. Talks to the coordinator's admin listener.
ban:
	$(GO) build -o bin/greyline-ban ./cmd/greyline-ban

test:
	$(GO) test ./...

race:
	$(GO) test -race -count=2 ./...

# Everything that can be checked without a game server or a player: the Go
# tests, the game-side C++ logic, and the seams between them.
check: race game-test check-maps check-seams

# The game's briefing logic, built with a plain g++. No Source, no VPC.
game-test:
	$(MAKE) -C ../../src/game/shared/greyline/tests test

# Every battlefield is playable, and — given a maps directory — actually
# installed:  make check-maps MAPS=~/.steam/steam/steamapps/common/tf2/tf/maps
check-maps:
	python3 tools/greyline_check_maps.py theater.industrial.json "$(MAPS)"

# The convar names the agent types exist in the game, and the stage kinds the
# war invents are ones the game can name.
check-seams:
	python3 tools/greyline_integration_check.py ../..

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
