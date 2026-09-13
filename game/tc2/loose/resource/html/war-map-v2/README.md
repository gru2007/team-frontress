# War Map v2

This replaces the old runtime influence-field renderer with a conventional map stack:

- **Leaflet 1.9.4** with `L.CRS.Simple` supplies camera, pan/zoom, layers and interaction;
- `maps/frontline.geojson` supplies authored sector polygons and supply-route geometry;
- `/v1/campaign` remains the source of dynamic ownership/front state;
- `/v1/campaign/command` remains the command endpoint (`deploy <node>`);
- the old implementation is preserved as `../campaign-legacy.html` and is used automatically if Leaflet or the geometry cannot load.

## Data separation

`services/coordinator/theater*.json` remains gameplay logic: nodes, adjacency, stages and battlefield pools. The GeoJSON is presentation only and joins to campaign data through `properties.node`, `properties.a` and `properties.b`.

A sector can therefore be redrawn without changing war rules, and war rules can change without regenerating a territory bitmap.

## Front line

A route whose endpoint owners differ is rendered as an active front. A sector present in `/v1/campaign.fronts` gets a contested border and battle marker. This deliberately avoids inferring political geography from distance-to-node.

## Leaflet packaging

The development branch loads Leaflet 1.9.4 from unpkg and falls back to the legacy map if that dependency is unavailable. Before shipping a release build, vendor the exact Leaflet JS/CSS distribution into this directory and change `index.html` to those local paths so the command board remains fully offline like the existing UI.
