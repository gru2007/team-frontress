# Frontress Game Coordinator

This service is the authoritative replacement for Valve's Team Fortress Game
Coordinator. The game still runs Valve's unmodified `CGCClient`, reliable jobs,
shared-object cache, party UI and lobby code. Only `ISteamGameCoordinator` is
replaced: native protobuf packets are exchanged with this service over
`POST /v1/gc/exchange`.

The service owns:

- authenticated client and dedicated-server GC sessions;
- party membership, invites, join requests, leadership and chat;
- matchmaking queues and standby/backfill requests;
- party and game-server lobby shared objects;
- reservation acknowledgement and match-result messages.

`tf2pickup-frontress` is the only match/server lifecycle backend. It persists
games, allocates serveme reservations, updates admission and reports terminal
state. The removed JSON queue API, game-server callbacks, greyline agent and
custom matchmaking RCON control plane are deliberately not supported.

## Configuration

Start from the generated config:

```text
go run ./cmd/coordinator -print-config > coordinator.json
```

At minimum configure:

```json
{
  "secret": "shared-game-server-secret",
  "tf2pickup": {
    "base_url": "http://tf2pickup:3000",
    "secret": "shared-tf2pickup-secret"
  },
  "auth": {
    "mode": "webapi",
    "steam_api_key": "...",
    "app_ids": [440]
  }
}
```

`TF2PICKUP_URL` and `TF2PICKUP_SECRET` override the corresponding file values.
Production clients authenticate with a Steam Web API ticket for the identity
`frontress-coordinator`. Dedicated servers authenticate with `secret` and send
their tf2pickup match ID.

## HTTP surface

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/gc/exchange` | Native Valve GC packet exchange for clients and servers |
| `GET` | `/v1/status` | Public queue, match-group and capacity summary |
| `GET` | `/v1/player/{steamid64}` | Public player progression summary |

The GC exchange envelope is JSON only as a transport wrapper. Each message is
the complete base64-encoded Valve protobuf packet, including its protobuf
header and job IDs. Matchmaking semantics do not exist in the game client or
in a parallel REST API.

Protocol 2 makes the HTTP transport loss-tolerant. A game process has a random
`instance_id`; each request carries a monotonic `client_sequence` and
acknowledges the last `server_sequence`. Both sides retain their in-flight
batch until it is acknowledged. Repeating a request after a timeout therefore
returns the same GC response without running queue, party or match operations
twice. Protocol 1 remains accepted only to permit a coordinator-first rolling
upgrade; current game builds always send protocol 2.

GC sessions and unacknowledged packets are intentionally kept in coordinator
memory, while durable match/server state remains in tf2pickup-frontress. Run a
single coordinator replica, or configure session affinity to the same replica;
after a coordinator restart Valve's GC client reconnects and reconstructs its
party/lobby cache from the authoritative matchmaking backend.

## Tests

```text
go test ./...
```

The game-side transport is built from the root Source solution. On Windows,
generate it with `src/createallprojects.bat`, then build the client and server
targets in `src/everything.sln`.
