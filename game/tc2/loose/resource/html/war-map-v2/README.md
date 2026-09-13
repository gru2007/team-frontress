# War Map v2

War Map v2 replaces the old runtime influence-field presentation with authored **GeoJSON sectors** while keeping the campaign protocol unchanged.

- `maps/frontline.geojson` contains presentation-only sector polygons;
- `/v1/campaign` remains the dynamic source of node ownership, operations and deployment;
- `/v1/campaign/command` remains the command endpoint (`deploy <node>`);
- supply routes are drawn from the campaign `edges` array, so adjacency still belongs to gameplay data;
- an edge whose endpoint owners differ is rendered as the active front;
- `../campaign-legacy.html` keeps the previous map intact.

## Why there is no map library dependency

The Source embedded UI must work offline. v2 therefore uses a local SVG renderer with a `viewBox` camera, mouse pan/zoom and GeoJSON-to-SVG conversion. There is no CDN and no third-party runtime dependency. GeoJSON remains the authoring format, so replacing the renderer later does not require changing campaign data.

## Data contract

GeoJSON sector features join to campaign nodes through `properties.id`. Coordinates are presentation coordinates in a 1000 × 620 plane. Unknown live node IDs are still rendered with a generated fallback polygon around their campaign `x`/`y` position, so a coordinator-side theater update does not make the UI disappear before its final art geometry lands.

The page preserves the legacy query contract:

- `?view=card` — compact menu card;
- `?view=full` — interactive theater;
- demo mode is the default;
- `?demo=0` opts into the live `/v1/campaign` feed.

`campaign.html?legacy=1` opens the old renderer without deleting v2 parameters.
