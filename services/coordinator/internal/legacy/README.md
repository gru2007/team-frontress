# Retired: the peer-to-peer coordinator

Everything under `internal/legacy` is the first prototype of GREYLINE FRONTRESS,
where a battle ran on one of the players' own machines. The coordinator elected
a host from the roster, told everyone else the address that host's engine
advertised, and stood the battle back up on somebody else when the host dropped.

It worked, and it was the wrong thing to build first. Host election, migration,
snapshot restore and result corroboration are all machinery for making an
untrusted, unreliable host trustworthy enough to move war state — three hard
problems in the way of finding out whether the war loop is fun at all.

The live coordinator runs battles on a pool of dedicated servers, with a
player-hosted battle server as an ordinary (untrusted, lower-priority) member
of that same pool when nothing dedicated is free — see the coordinator README
and `docs/GREYLINE-WAR.md` for that design. Nothing in the running program
imports this directory any more, and the client's raw-TCP side of it is off by
default (`greyline_gc_enable 0`).

It is kept because the problems it solves are real ones the project may come
back to — community-hosted nodes with no fixed address are exactly the P2P case
— and because the host-election scoring and the corroboration rules are worth
more as working code than as a memory. The host-election scoring already made
that trip: it now lives at `internal/hostelect` (moved out of this directory,
still imported here by `legacy/gc`) and is what `internal/mm` uses to elect a
player-hosted battle server.

| Package | What it was |
| --- | --- |
| `legacy/gc` | The framed-protobuf coordinator: sessions, queue, match lifecycle, migration |
| `legacy/war` | The first war model: one front, battle points, a flat map pool |
| `legacy/testdata` | World files for that model |

The wire protocol it speaks (`internal/wire`, `proto/greyline.proto`) is still
compiled by both halves, so the schema check in CI still covers it.
