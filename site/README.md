# team-frontress.org

The landing page, on Cloudflare Workers. The page is static (`public/`); the
worker (`src/worker.js`) only redirects `www.` to the bare domain and serves
`/api/status` and `/api/war`: the coordinator's public `/v1/status` and
`/v1/war`, trimmed to what the page shows and cached at the edge (15 s and
30 s), so visitors never reach the GC directly. When the GC is down the page
says so and still works.

```sh
npm install
npm run dev      # http://localhost:8787, with live data from gc.team-frontress.org
npm run deploy   # needs `npx wrangler login` once
```

- **Domain.** `wrangler.jsonc` attaches the worker to `team-frontress.org` and
  `www.team-frontress.org` as Custom Domains. Cloudflare creates the DNS
  records on deploy. If the zone already has A/AAAA/CNAME records for those
  names (the old "coming soon" page), delete them first, or the deploy refuses.
  `mm.` and `gc.` are not touched.
- **Coordinator.** `GC_URL` in `wrangler.jsonc`.
- **Texts.** English is in `public/index.html`; Russian is the `ru` table at
  the top of `public/app.js` under the same `data-i18n` keys. The language
  follows the browser, `?lang=ru|en` or the switch in the corner.
- **The war planet** (`public/planet.js`, `public/planet-worker.js`) is a port
  of the game's `<warmap>` (`src/frontress_ui/fui_warmap.cpp`): the same
  generated planet, hex territory, front line, roads and offensive arrows, on
  WebGL 2. Change one, change the other. Without WebGL 2 the page falls back
  to a flat map.
- **Art.** `hero.jpg` is the Steam page background, `logo.png` the project logo
  (`.github/logo.png`). Colours come from the in-game UI
  (`game/tc2/loose/resource/fui/style/base.rcss`).
