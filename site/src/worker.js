// team-frontress.org: the landing page, served from Cloudflare.
//
// The page itself is static (public/). The worker only adds:
//   - www.team-frontress.org -> team-frontress.org
//   - /api/status and /api/war: the coordinator's public numbers, trimmed to
//     what the page shows and cached at the edge, so visitors never hit the
//     GC directly and a busy page costs it one request per cache period.

const routes = {
  "/api/status": { path: "/v1/status", ttl: 15, shape: shapeStatus },
  "/api/war": { path: "/v1/war", ttl: 30, shape: shapeWar },
};

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.hostname.startsWith("www.")) {
      url.hostname = url.hostname.slice(4);
      return Response.redirect(url.toString(), 301);
    }
    const route = routes[url.pathname];
    if (route) {
      return api(request, env, ctx, route);
    }
    if (url.pathname.startsWith("/api/")) {
      return json({ error: "not found" }, 404, 60);
    }
    return env.ASSETS.fetch(request);
  },
};

async function api(request, env, ctx, route) {
  if (request.method !== "GET" && request.method !== "HEAD") {
    return json({ error: "method not allowed" }, 405, 0);
  }
  const cache = caches.default;
  const key = new Request(new URL(route.path, new URL(request.url).origin), { method: "GET" });
  const hit = await cache.match(key);
  if (hit) {
    return hit;
  }

  let body;
  let status = 200;
  let ttl = route.ttl;
  try {
    const gc = await fetch(new URL(route.path, env.GC_URL), {
      headers: { accept: "application/json", "user-agent": "team-frontress-site" },
      signal: AbortSignal.timeout(5000),
    });
    if (gc.status === 404 && route.path === "/v1/war") {
      body = { running: false };
    } else if (!gc.ok) {
      throw new Error(`GC answered ${gc.status}`);
    } else {
      body = route.shape(await gc.json());
    }
  } catch (err) {
    console.warn("coordinator unreachable", route.path, String(err));
    body = { offline: true };
    status = 503;
    ttl = 10; // still cached briefly, so an outage is not a stampede
  }

  const res = json(body, status, ttl);
  ctx.waitUntil(cache.put(key, res.clone()));
  return res;
}

function json(body, status, ttl) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": ttl > 0 ? `public, max-age=${ttl}` : "no-store",
      "access-control-allow-origin": "*",
    },
  });
}

const num = (v) => (typeof v === "number" && Number.isFinite(v) ? v : 0);
const str = (v) => (typeof v === "string" ? v : "");
const list = (v) => (Array.isArray(v) ? v : []);

function shapeStatus(s) {
  return {
    online: num(s.online),
    in_menu: num(s.in_menu),
    in_queue: num(s.in_queue),
    in_match: num(s.in_match),
    live_matches: num(s.live_matches),
    servers: num(s.servers),
    free_servers: num(s.free_servers),
    clans: num(s.clans),
    queues: list(s.groups).map((g) => ({
      name: str(g.name),
      mode: str(g.mode),
      queued: num(g.queued),
      matches: num(g.matches),
      max: num(g.max),
      maps: list(g.maps).length,
    })),
    at: str(s.generated_at),
  };
}

function shapeWar(w) {
  return {
    running: true,
    name: str(w.name),
    day: num(w.day),
    winner: str(w.winner),
    next_campaign_at: num(w.next_campaign_at),
    red: num(w.red),
    red_fronts: num(w.red_fronts),
    blu_fronts: num(w.blu_fronts),
    battles: num(w.battles),
    soldiers: num(w.soldiers),
    order: str(w.top),
    order_xp: num(w.order_xp),
    sectors: list(w.fronts).map((n) => ({
      id: str(n.id),
      name: str(n.name),
      hq: Boolean(n.hq),
      x: num(n.x),
      y: num(n.y),
      owner: str(n.owner),
      control: num(n.control),
      attacker: str(n.attacker),
      from: str(n.from),
      stage: num(n.stage),
      stages: num(n.stages),
      mode: str(n.mode),
      map: str(n.map),
      banner: n.banner && n.banner.tag ? { tag: str(n.banner.tag), color: str(n.banner.color) } : null,
    })),
    links: list(w.links).map((l) => ({ a: str(l.a), b: str(l.b), side: str(l.side) })),
    ops: list(w.ops).map((o) => ({
      node: str(o.node),
      name: str(o.name),
      attacker: str(o.attacker),
      from: str(o.from_name),
      stage: num(o.stage),
      stages: num(o.stages),
      plan: list(o.plan).map(str),
      mode: str(o.mode),
      map: str(o.map),
      battles: num(o.battles),
      momentum: num(o.momentum),
      momentum_max: num(o.momentum_max),
      conditions: list(o.conditions).map(str),
      order: Boolean(o.order),
      hq: Boolean(o.hq),
    })),
    live: list(w.live).map((b) => ({
      name: str(b.name),
      map: str(b.map),
      stage_kind: str(b.stage_kind),
      players: num(b.players),
      max: num(b.max),
    })),
    events: list(w.events)
      .slice(0, 8)
      .map((e) => ({ kind: str(e.kind), side: str(e.side), name: str(e.name), stage_kind: str(e.stage_kind), ago: num(e.ago) })),
  };
}
