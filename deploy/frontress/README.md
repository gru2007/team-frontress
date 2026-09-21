# Frontress stack

This compose project joins the three repositories without merging their source
trees:

```text
Team Frontress client -> frontress-gateway -> tf2pickup-frontress -> serveme-frontress
```

The gateway is the Frontress Game Coordinator: it owns Steam-authenticated GC
sessions, party/queue/lobby state, map/war policy and backfill placement.
`tf2pickup-frontress` owns durable games, player history, ELO and server
lifecycle. `serveme-frontress` owns reservation and game-server containers.
The dedicated server reports lobby status and results through the GC transport;
RCON remains an infrastructure/configuration channel rather than match state.

## Start

1. Copy `serveme-frontress/.env.example` to `serveme-frontress/.env` and fill its required values.
2. Add the variables listed below to that file.
3. From this directory run `docker compose --env-file ../../../serveme-frontress/.env up -d --build`.

Required integration variables:

```dotenv
FRONTRESS_GATEWAY_SECRET=generate-one-long-random-value
FRONTRESS_COORDINATOR_URL=https://gc.example.org
FRONTRESS_COORDINATOR_SECRET=generate-a-distinct-game-server-secret
TF2PICKUP_KEY_STORE_PASSPHRASE=generate-another-long-random-value
TF2PICKUP_GAME_SERVER_SECRET=generate-another-long-random-value
TF2PICKUP_LOG_ADDRESS=public-or-host-address-reachable-from-game-servers
SERVEME_PUBLIC_HOST=serveme.example.org
SERVEME_TF_API_KEY=trusted-api-user-key
# Reserved until the playtest-AppID inventory source is enabled:
STEAM_INVENTORY_PUBLISHER_KEY=
```

`SERVEME_PUBLIC_HOST` intentionally has no `https://`: the upstream
`@tf2pickup-org/serveme-tf-client` prepends HTTPS itself. The serveme API user
must be trusted when the fork is configured to reserve Frontress containers.
`FRONTRESS_IDLE_END_SECONDS` and `FRONTRESS_MAX_MATCH_SECONDS` are optional;
they default to 300 and 10800 seconds respectively.

Expose `27100/tcp` to game clients through TLS, `3001/tcp` (normally through a
TLS reverse proxy) for the tf2pickup UI/API, and `9871/udp` to game-server
containers. The client defaults to `https://gc.team-frontress.org`; the reverse
proxy must present a certificate valid for that exact hostname and route
`/v1/gc/` to port `27100`. Do not expose the client exchange over plain HTTP:
it carries a Steam Web API authentication ticket. Set
`FRONTRESS_COORDINATOR_URL` to the public GC URL so serveme passes the same
endpoint and secret into each dedicated server. The serveme ports remain
governed by its own `.env`.
