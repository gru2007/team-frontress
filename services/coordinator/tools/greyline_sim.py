#!/usr/bin/env python3
"""Drive a real coordinator through a fake war.

Everything here is a real HTTP client: real sessions, real DEPLOY, a real server
pool registration, real results. The only thing being faked is the game — no
srcds, no game client, no map ever loads.

That is the point. The question the MVP has to answer is whether the war loop
works: does a battle move a front, does a front decide the next battle, does a
capture move the war along the graph, does a campaign end. None of that needs a
map to load, and waiting for one would make it take an evening to find out.

    ./tools/greyline_sim.py --battles 20 --players 4

Run it against a coordinator started with the testbed config:

    ./bin/coordinator -config gc.test.json

See docs/GREYLINE-MVP-TESTING.md for what to look for in the output.
"""

import argparse
import json
import random
import sys
import time
import urllib.error
import urllib.request


class GCError(Exception):
    """The coordinator refused a request."""

    def __init__(self, code, detail):
        super().__init__(f"{code}: {detail}")
        self.code = code
        self.detail = detail


class GC:
    """A tiny HTTP client for the coordinator."""

    def __init__(self, base):
        self.base = base.rstrip("/")

    def call(self, method, path, body=None, token=None, timeout=40):
        url = self.base + path
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if token:
            req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                if resp.status == 204:
                    return None
                raw = resp.read()
                return json.loads(raw) if raw else None
        except urllib.error.HTTPError as e:
            detail = e.read().decode(errors="replace").strip()
            raise GCError(e.code, detail or f"{method} {path}")
        except urllib.error.URLError as e:
            raise SystemExit(f"cannot reach the coordinator at {self.base}: {e.reason}")


class Player:
    """A fake game client that only ever presses DEPLOY."""

    queued = False

    def __init__(self, gc, steam_id, side):
        self.gc = gc
        self.steam_id = steam_id
        self.side = side
        self.seq = 0
        reply = gc.call("POST", "/api/v1/client/hello", {
            "steam_id": steam_id,
            "name": f"merc{steam_id}",
            "side": side,
            "client_version": "sim",
        })
        self.token = reply["token"]

    def deploy(self):
        """Press DEPLOY, once. A real client does not re-press it while queued,
        and neither does this one: the coordinator holds out for a bigger battle
        based on how long the first player has been waiting."""
        if self.queued:
            self.poll()
            return True
        try:
            self.gc.call("POST", "/api/v1/client/deploy", {"accept_contract": True}, self.token)
        except GCError as e:
            if e.code == 409:
                # Between campaigns, or every front resolved at once. Not an
                # error: the war is simply not taking deployments this second.
                return False
            raise
        self.queued = True
        return True

    def drain_battle_events(self):
        """Watch for the battle ending, which puts this player back on the map."""
        for ev in self.poll():
            if ev["type"] == "match_over":
                self.queued = False

    def poll(self):
        reply = self.gc.call(
            "GET", f"/api/v1/client/poll?since={self.seq}&wait=0", token=self.token)
        self.seq = reply.get("seq", self.seq)
        return reply.get("events") or []


class Server:
    """A fake dedicated server: it takes assignments and reports results."""

    def __init__(self, gc, pool_key, name, connect):
        self.gc = gc
        reply = gc.call("POST", "/api/v1/servers/register", {
            "name": name,
            "connect_address": connect,
            "capacity": 24,
            "agent_version": "sim",
        }, token=pool_key)
        self.id = reply["server_id"]
        self.token = reply["server_token"]

    def next_assignment(self, wait=2):
        cmd = self.gc.call(
            "GET", f"/api/v1/servers/poll?wait={wait}&server_id={self.id}",
            token=self.token, timeout=wait + 10)
        if not cmd or cmd.get("type") != "assign":
            return None
        return cmd["assignment"]

    def state(self, match_id, state):
        self.gc.call("POST", f"/api/v1/servers/state?server_id={self.id}",
                     {"match_id": match_id, "state": state}, self.token)

    def result(self, match_id, outcome, red, blu):
        return self.gc.call("POST", f"/api/v1/servers/result?server_id={self.id}", {
            "match_id": match_id,
            "outcome": outcome,
            "red_score": red,
            "blu_score": blu,
            "duration_s": 900,
        }, self.token)


def world_line(gc):
    """One line of scoreboard: who holds what, and where the fighting is."""
    world = gc.call("GET", "/api/v1/world")["world"]
    owners = {n["id"]: n["owner"] for n in world["nodes"]}
    counts = world["node_count"]
    fronts = ", ".join(
        f"{f['name']} [{f['attacker']} {f['stage'] + 1}/{len(f['plan'])}]"
        for f in world["fronts"]) or "none"
    return (f"RED {counts.get('RED', 0)} · BLU {counts.get('BLU', 0)} · "
            f"neutral {counts.get('NONE', 0)} | {fronts}"), owners, world


def pick_outcome(mode, assignment):
    """Who wins this one."""
    if mode == "random":
        return random.choice(["RED_WIN", "BLU_WIN"])
    if mode == "stalemate":
        return "STALEMATE"
    # "attacker" and "defender" are stated in war sides; the server reports in
    # in-game teams, so translate the same way a real agent's scoreboard would.
    attacker = assignment["briefing"]["attacker"]
    defender = assignment["briefing"]["defender"]
    want = attacker if mode == "attacker" else defender
    roster = assignment["roster"]
    for entry in roster:
        if entry["side"] == want:
            return "RED_WIN" if entry["team"] == "RED" else "BLU_WIN"
    return "RED_WIN" if want == "RED" else "BLU_WIN"


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--gc", default="http://127.0.0.1:27100", help="coordinator base URL")
    ap.add_argument("--key", default="testbed-pool-key-change-me", help="server pool key")
    ap.add_argument("--players", type=int, default=4, help="how many fake mercenaries")
    ap.add_argument("--servers", type=int, default=1, help="how many fake game servers")
    ap.add_argument("--battles", type=int, default=10, help="how many battles to play")
    ap.add_argument("--winner", default="random",
                    choices=["random", "attacker", "defender", "stalemate"],
                    help="who wins every battle; 'attacker' is the fastest way to see a capture")
    ap.add_argument("--seed", type=int, default=None, help="make a run reproducible")
    args = ap.parse_args()

    if args.seed is not None:
        random.seed(args.seed)

    gc = GC(args.gc)
    status = gc.call("GET", "/api/v1/status")
    print(f"coordinator: {status['theater']['name']} · {status['campaign']['name']} "
          f"({status['campaign']['status']})")

    servers = [Server(gc, args.key, f"sim-{i}", f"10.0.0.{i + 1}:27015")
               for i in range(args.servers)]
    print(f"pool: {len(servers)} fake server(s) registered")

    players = []
    for i in range(args.players):
        side = "RED" if i % 2 == 0 else "BLU"
        players.append(Player(gc, 76561197960265728 + 1000 + i, side))
    print(f"players: {len(players)} online "
          f"({sum(1 for p in players if p.side == 'RED')} RED, "
          f"{sum(1 for p in players if p.side == 'BLU')} BLU)")

    line, owners_before, _ = world_line(gc)
    print(f"\nbefore: {line}\n")

    played = 0
    idle_rounds = 0
    while played < args.battles and idle_rounds < 30:
        # A list, not a generator: any() short-circuits, and everybody has to
        # press DEPLOY or the queue never fills.
        deployed = [p.deploy() for p in players]
        if not any(deployed):
            campaign = gc.call("GET", "/api/v1/status")["campaign"]
            if campaign["status"] != "ACTIVE":
                print(f"\n{campaign['name']} is over — {campaign['winner']} won it in "
                      f"{campaign['battles']} battles. The next one opens after the "
                      f"armistice; run this again then.")
                break
            idle_rounds += 1
            time.sleep(0.5)
            continue

        got_one = False
        for server in servers:
            assignment = server.next_assignment(wait=2)
            if not assignment:
                continue
            got_one = True
            b = assignment["briefing"]
            server.state(assignment["match_id"], "ready")
            server.state(assignment["match_id"], "live")

            outcome = pick_outcome(args.winner, assignment)
            reply = server.result(assignment["match_id"], outcome, 3, 1)
            update = (reply or {}).get("war_update") or {}
            played += 1

            swapped = any(e["side"] != e["team"] for e in assignment["roster"])
            print(f"[{played:>3}] {b['front_name']}  {b['stage_kind']} "
                  f"{b['stage']}/{b['stage_count']}  {assignment['map']} ({assignment['mode']})"
                  + ("  [colours swapped]" if swapped else ""))
            print(f"      -> {update.get('headline', 'battle did not count')}")
            if update.get("node_captured"):
                print(f"      ** {update['new_owner']} took {update['node_id']}")
            if update.get("offensive_collapsed"):
                print("      ** the offensive collapsed")
            if update.get("campaign_over"):
                print(f"      ** CAMPAIGN OVER — {update['campaign_winner']} wins")

        for p in players:
            p.drain_battle_events()

        if got_one:
            idle_rounds = 0
        else:
            idle_rounds += 1
            time.sleep(0.5)

    if idle_rounds >= 30:
        print("\nno battle was formed for a while. Check that the coordinator has "
              "enough players queued (--players) and that min_team_size in the "
              "config is small enough.", file=sys.stderr)

    line, owners_after, world = world_line(gc)
    print(f"\nafter:  {line}")

    changed = {k: (owners_before[k], v) for k, v in owners_after.items()
               if owners_before.get(k) != v}
    if changed:
        print("\nterritory changed hands:")
        for node, (was, now) in changed.items():
            print(f"  {node}: {was} -> {now}")
    else:
        print("\nno territory changed hands yet — try --winner attacker --battles 6")

    print("\nthe war's own account of it:")
    for ev in gc.call("GET", "/api/v1/world/timeline?limit=200")["events"][-12:]:
        print(f"  {ev['at'][11:19]}  {ev['headline']}")


if __name__ == "__main__":
    try:
        main()
    except GCError as e:
        raise SystemExit(f"coordinator refused the request — {e}")
