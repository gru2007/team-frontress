// The war, drawn as a planet: a port of the game's <warmap>
// (src/frontress_ui/fui_warmap.cpp) to WebGL 2 and a 2D canvas.
//
// The theater (sectors in a 1140 x 640 plane, as the coordinator has them) is
// wrapped onto the front of a generated globe (planet-worker.js), lit from the
// upper left, with clouds, an atmosphere and stars. Territory is derived, as in
// the game: the plane is tiled with hexes and each hex belongs to the nearest
// sector, so the ground, the borders and the front line follow the owners.
// Operations are arrows from the sector they push from, thicker at each stage,
// with chevrons running along them; their targets pulse.
//
// Drag turns it (it keeps spinning a little when let go), + and - zoom, and it
// turns to the sector you pick. Left alone, it sways slowly.
//
//   const planet = WarPlanet.create(element, { onSelect, onHover });
//   planet.setWar(war);   // { sectors: [{id,x,y,owner,attacker,from,stage,hq,name,mode}], links }
//   planet.select(id);
//
// create() returns null without WebGL 2; the page then keeps its flat map.

const DEG = Math.PI / 180;
const SQRT3 = 1.7320508;
const LON_SPAN = 124; // degrees of longitude the theater covers
const LAT_SPAN = 70;
const HEX_R = 19;
const TEX_SCALE = 2; // theater units -> texture pixels
// Screen widths in the game are in units of R/300; on the texture they are in
// theater units, which are R * (124 deg in rad) / 1140 pixels at the centre.
const TO_THEATER = 1140 / (300 * LON_SPAN * DEG);

const RED = [214, 66, 52, 92];
const BLU = [66, 130, 196, 92];
const RED_ARROW = [236, 92, 66];
const BLU_ARROW = [98, 162, 226];
const RED_ROAD = "rgba(236,98,80,0.86)";
const BLU_ROAD = "rgba(116,172,226,0.86)";
const GOLD = "rgb(255,205,90)";

const reduceMotion = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;

function hash(a, b) {
  let h = (Math.imul(a, 73856093) ^ Math.imul(b, 19349663)) >>> 0;
  h ^= h >>> 13;
  h = Math.imul(h, 0x5bd1e995) >>> 0;
  h ^= h >>> 15;
  return h >>> 0;
}

const vertexSrc = `#version 300 es
in vec2 aPos;
uniform vec2 uSize;
out vec2 vPos;
void main() {
  vPos = vec2((aPos.x * 0.5 + 0.5) * uSize.x, (0.5 - aPos.y * 0.5) * uSize.y);
  gl_Position = vec4(aPos, 0.0, 1.0);
}`;

// The globe, pixel by pixel: unproject to longitude and latitude, then the
// game's layers in its order: ground (lit), grid, territory, clouds, lines.
const fragmentSrc = `#version 300 es
precision highp float;
in vec2 vPos;
out vec4 outColor;
uniform vec3 uView;   // centre x, y and radius, in canvas pixels
uniform vec2 uRot;    // longitude facing us, tilt (degrees)
uniform float uCloud; // cloud drift (degrees)
uniform float uReady; // the surface has arrived
uniform sampler2D uSurface, uClouds, uTerritory, uLines;
const float DEG = 0.017453292519943295;

float gridLine(float v, float spacing) {
  float d = abs(fract(v / spacing + 0.5) - 0.5) * spacing;
  float w = min(fwidth(v), 3.0);
  return 1.0 - smoothstep(0.0, w * 1.2, d);
}

void main() {
  float X = (vPos.x - uView.x) / uView.z;
  float Y = (uView.y - vPos.y) / uView.z;
  float d = X * X + Y * Y;
  if (d >= 1.0) discard;
  float Z = sqrt(1.0 - d);
  float tilt = uRot.y * DEG;
  float y0 = Y * cos(tilt) + Z * sin(tilt);
  float z0 = -Y * sin(tilt) + Z * cos(tilt);
  float lat = asin(clamp(y0, -1.0, 1.0)) / DEG;
  float lon = atan(X, z0) / DEG + uRot.x;
  float lonW = mod(lon + 180.0, 360.0) - 180.0;
  float light = 0.28 + 0.72 * max(0.0, -0.55 * X + 0.55 * Y + 0.63 * Z);
  float v = (90.0 - lat) / 180.0;

  vec3 ground = mix(vec3(0.063, 0.133, 0.172), texture(uSurface, vec2(fract((lon + 180.0) / 360.0), v)).rgb, uReady);
  vec3 col = ground * vec3(1.0, 0.973, 0.925) * (0.1 + 0.95 * light);

  float grid = 0.0;
  if (abs(lat) <= 75.5) grid = gridLine(lat, 15.0);
  // Meridians, but not where they bunch up at the poles or at atan's seam.
  if (fwidth(lon) < 2.0) grid = max(grid, gridLine(lon, 15.0) * (1.0 - smoothstep(78.0, 86.0, abs(lat))));
  col = mix(col, vec3(1.0, 0.886, 0.667), grid * 0.086 * min(1.0, Z * 3.0));

  vec2 tuv = vec2(lonW / ${LON_SPAN.toFixed(1)} + 0.5, 0.5 - lat / ${LAT_SPAN.toFixed(1)});
  bool inTheater = tuv.x >= 0.0 && tuv.x <= 1.0 && tuv.y >= 0.0 && tuv.y <= 1.0;
  if (inTheater) {
    vec4 ter = texture(uTerritory, tuv) * min(1.0, Z * 4.0);
    col = col * (1.0 - ter.a) + ter.rgb * (0.55 + 0.6 * light);
  }

  vec4 cloud = texture(uClouds, vec2(fract((lon - uCloud + 180.0) / 360.0), v));
  float ca = 0.55 * clamp(0.25 + light, 0.0, 1.0) * uReady;
  col = col * (1.0 - cloud.a * ca) + cloud.rgb * vec3(1.0, 0.973, 0.925) * ca;

  if (inTheater) {
    vec4 ln = texture(uLines, tuv) * min(1.0, Z * 3.0);
    col = col * (1.0 - ln.a) + ln.rgb;
  }

  float a = clamp((1.0 - sqrt(d)) * uView.z, 0.0, 1.0);
  outColor = vec4(col * a, a);
}`;

function compile(gl, type, src) {
  const s = gl.createShader(type);
  gl.shaderSource(s, src);
  gl.compileShader(s);
  if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(s) || "shader");
  return s;
}

export class WarPlanet {
  static create(root, opts = {}) {
    try {
      const planet = new WarPlanet(root, opts);
      return planet.gl ? planet : null;
    } catch (err) {
      console.warn("planet unavailable", err);
      return null;
    }
  }

  constructor(root, { onSelect = () => {}, onHover = () => {} } = {}) {
    this.root = root;
    this.onSelect = onSelect;
    this.onHover = onHover;
    this.back = document.createElement("canvas");
    this.glCanvas = document.createElement("canvas");
    this.front = document.createElement("canvas");
    this.markers = document.createElement("div");
    this.back.className = "planet-back";
    this.glCanvas.className = "planet-globe";
    this.front.className = "planet-front";
    this.markers.className = "planet-markers";
    const gl = this.glCanvas.getContext("webgl2", { premultipliedAlpha: true, antialias: false });
    if (!gl) return;
    this.gl = gl;
    root.append(this.back, this.glCanvas, this.front, this.markers);

    this.sectors = [];
    this.links = [];
    this.byId = new Map();
    this.hexes = [];
    this.regions = new Map();
    this.outlines = new Map();
    this.selected = "";
    this.hover = "";
    this.markerEls = new Map();
    this.stars = Array.from({ length: 220 }, (_, i) => {
      const h = hash(i, 977);
      return { x: (h & 0xffff) / 65535, y: ((h >>> 16) & 0xffff) / 65535, size: 0.4 + ((hash(i, 31) & 255) / 255) * 1.4, phase: (hash(i, 7) & 255) / 40 };
    });

    // The view: where the planet faces, how big it is.
    this.lon = 0;
    this.lat = 0;
    this.targetLon = 0;
    this.targetLat = 0;
    this.sway = 0;
    this.zoom = 1;
    this.spinLon = 0;
    this.spinLat = 0;
    this.last = 0;
    this.idleSince = 0;
    this.dragging = false;
    this.dragged = false;

    this.initGL();
    this.loadSurface();
    this.bindInput();
    this.resize();
    new ResizeObserver(() => this.resize()).observe(root);
    this.visible = true;
    new IntersectionObserver((entries) => {
      this.visible = entries.some((e) => e.isIntersecting);
      if (this.visible) this.kick();
    }).observe(root);
    document.addEventListener("visibilitychange", () => this.kick());
    this.kick();
  }

  // GL ----------------------------------------------------------------------

  initGL() {
    const gl = this.gl;
    const prog = gl.createProgram();
    gl.attachShader(prog, compile(gl, gl.VERTEX_SHADER, vertexSrc));
    gl.attachShader(prog, compile(gl, gl.FRAGMENT_SHADER, fragmentSrc));
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(prog) || "link");
    this.prog = prog;
    gl.useProgram(prog);
    const buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
    const loc = gl.getAttribLocation(prog, "aPos");
    gl.enableVertexAttribArray(loc);
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
    this.u = {};
    for (const name of ["uSize", "uView", "uRot", "uCloud", "uReady", "uSurface", "uClouds", "uTerritory", "uLines"]) {
      this.u[name] = gl.getUniformLocation(prog, name);
    }
    this.tex = {};
    ["surface", "clouds", "territory", "lines"].forEach((name, unit) => {
      const t = gl.createTexture();
      gl.activeTexture(gl.TEXTURE0 + unit);
      gl.bindTexture(gl.TEXTURE_2D, t);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array(4));
      const wrap = name === "surface" || name === "clouds" ? gl.REPEAT : gl.CLAMP_TO_EDGE;
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, wrap);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
      this.tex[name] = { t, unit };
    });
    gl.uniform1i(this.u.uSurface, 0);
    gl.uniform1i(this.u.uClouds, 1);
    gl.uniform1i(this.u.uTerritory, 2);
    gl.uniform1i(this.u.uLines, 3);
    this.ready = 0;
  }

  upload(name, source, w, h, premultiply) {
    const gl = this.gl;
    const { t, unit } = this.tex[name];
    gl.activeTexture(gl.TEXTURE0 + unit);
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.pixelStorei(gl.UNPACK_PREMULTIPLY_ALPHA_WEBGL, premultiply);
    if (w) gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, w, h, 0, gl.RGBA, gl.UNSIGNED_BYTE, source);
    else gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source);
    gl.generateMipmap(gl.TEXTURE_2D);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR);
    gl.pixelStorei(gl.UNPACK_PREMULTIPLY_ALPHA_WEBGL, false);
  }

  loadSurface() {
    const worker = new Worker(new URL("./planet-worker.js", import.meta.url));
    worker.onmessage = (e) => {
      const { w, h, surface, clouds } = e.data;
      this.upload("surface", new Uint8Array(surface.buffer), w, h, false);
      this.upload("clouds", new Uint8Array(clouds.buffer), w, h, false);
      this.readyAt = this.now();
      worker.terminate();
      this.kick();
    };
    worker.postMessage({ w: 1024, h: 512 });
  }

  // State -------------------------------------------------------------------

  setWar(war) {
    const sectors = (war.sectors || []).map((s) => ({ ...s, weight: s.hq ? 1.25 : 1 }));
    const links = war.links || [];
    const key = JSON.stringify([sectors.map((s) => [s.id, s.x, s.y, s.owner, s.attacker, s.from, s.stage, s.name]), links]);
    if (key === this.key) return;
    this.key = key;
    this.sectors = sectors;
    this.links = links;
    this.byId = new Map(sectors.map((s) => [s.id, s]));
    this.buildRegions();
    this.paintTheater();
    this.buildMarkers();
    if (this.selected && !this.byId.has(this.selected)) this.selected = "";
    this.kick();
  }

  select(id, turn = true) {
    if (!this.byId.has(id)) return;
    this.selected = id;
    if (turn) {
      const s = this.byId.get(id);
      const [lon, lat] = this.toLonLat(s.x, s.y);
      // The nearest turn to it, not back round the planet.
      this.targetLon = lon + 360 * Math.round((this.lon - lon) / 360);
      this.targetLat = Math.min(30, Math.max(-30, lat * 0.6));
      this.idleSince = this.now();
    }
    this.updateMarkerClasses();
    this.onSelect(id);
    this.kick();
  }

  setHover(id) {
    if (id === this.hover) return;
    this.hover = id;
    this.updateMarkerClasses();
    this.onHover(id);
    this.kick();
  }

  // Geometry ----------------------------------------------------------------

  now() {
    return performance.now() / 1000;
  }

  resize() {
    const rect = this.root.getBoundingClientRect();
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    this.w = rect.width;
    this.h = rect.height;
    this.dpr = dpr;
    for (const c of [this.back, this.glCanvas, this.front]) {
      c.width = Math.max(1, Math.round(rect.width * dpr));
      c.height = Math.max(1, Math.round(rect.height * dpr));
    }
    this.kick();
  }

  fit() {
    // On a wide view the planet sits left of centre, clear of the sector card.
    this.cx = this.w * (this.w > 900 ? 0.42 : 0.5);
    this.cy = this.h * 0.5;
    this.R = Math.min(this.w, this.h) * 0.47 * this.zoom;
  }

  toLonLat(x, y) {
    return [(x / 1140 - 0.5) * LON_SPAN, (0.5 - y / 640) * LAT_SPAN];
  }

  projectLonLat(lon, lat) {
    const l = (lon - this.lon - this.sway) * DEG, p = lat * DEG, tilt = this.lat * DEG;
    const x = Math.cos(p) * Math.sin(l);
    const y = Math.sin(p);
    const z = Math.cos(p) * Math.cos(l);
    const y2 = y * Math.cos(tilt) - z * Math.sin(tilt);
    const z2 = y * Math.sin(tilt) + z * Math.cos(tilt);
    const light = 0.28 + 0.72 * Math.max(0, x * -0.55 + y2 * 0.55 + z2 * 0.63);
    return { x: this.cx + x * this.R, y: this.cy - y2 * this.R, z: z2, light };
  }

  project(x, y) {
    const [lon, lat] = this.toLonLat(x, y);
    return this.projectLonLat(lon, lat);
  }

  unproject(sx, sy) {
    const X = (sx - this.cx) / this.R, Y = (this.cy - sy) / this.R;
    const d = X * X + Y * Y;
    if (d >= 1) return null;
    const Z = Math.sqrt(1 - d), tilt = this.lat * DEG;
    const y0 = Y * Math.cos(tilt) + Z * Math.sin(tilt);
    const z0 = -Y * Math.sin(tilt) + Z * Math.cos(tilt);
    const lat = Math.asin(Math.min(1, Math.max(-1, y0))) / DEG;
    let lon = Math.atan2(X, z0) / DEG + this.lon + this.sway;
    lon = ((((lon + 180) % 360) + 360) % 360) - 180;
    return { x: (lon / LON_SPAN + 0.5) * 1140, y: (0.5 - lat / LAT_SPAN) * 640 };
  }

  nearest(x, y) {
    let best = -1, bestD = Infinity;
    this.sectors.forEach((s, i) => {
      const d = Math.hypot(s.x - x, s.y - y) / s.weight;
      if (d < bestD) {
        bestD = d;
        best = i;
      }
    });
    return best;
  }

  sectorAt(sx, sy) {
    const p = this.unproject(sx, sy);
    if (!p || p.x < 0 || p.y < 0 || p.x > 1140 || p.y > 640) return "";
    const i = this.nearest(p.x, p.y);
    return i >= 0 ? this.sectors[i].id : "";
  }

  // Which sector every hex belongs to, and the edges between sectors.
  buildRegions() {
    this.hexes = [];
    this.borders = [];
    this.frontSegs = [];
    this.regions = new Map();
    this.outlines = new Map();
    if (!this.sectors.length) return;
    const W = 1140, H = 640, R = HEX_R;
    const hexW = SQRT3 * R, row = 1.5 * R;
    const cols = Math.ceil(W / hexW) + 1, rows = Math.ceil(H / row) + 1;
    const grid = new Int32Array(cols * rows);
    const centre = (c, r) => [c * hexW + (r & 1 ? hexW * 0.5 : 0), r * row];
    const push = (map, key, v) => {
      if (!map.has(key)) map.set(key, []);
      map.get(key).push(v);
    };
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        const [x, y] = centre(c, r);
        const sector = this.nearest(x, y);
        grid[r * cols + c] = sector;
        const edge = Math.min(x, W - x, y, H - y);
        const alpha = (0.35 + 0.65 * Math.min(1, Math.max(0, edge / 90))) * (0.8 + 0.35 * ((hash(c, r) & 255) / 255));
        push(this.regions, this.sectors[sector].id, this.hexes.length);
        this.hexes.push({ x, y, sector, blu: this.sectors[sector].owner === "blu", alpha });
      }
    }
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        const [x, y] = centre(c, r);
        const here = grid[r * cols + c];
        const odd = r & 1;
        const nb = [[c + odd, r - 1], [c + 1, r], [c + odd, r + 1]];
        nb.forEach(([nc, nr], e) => {
          if (nc < 0 || nr < 0 || nc >= cols || nr >= rows) return;
          const there = grid[nr * cols + nc];
          if (there === here) return;
          const a0 = (-90 + 60 * e) * DEG, a1 = (-90 + 60 * (e + 1)) * DEG;
          const seg = [x + Math.cos(a0) * R, y + Math.sin(a0) * R, x + Math.cos(a1) * R, y + Math.sin(a1) * R];
          (this.sectors[here].owner !== this.sectors[there].owner ? this.frontSegs : this.borders).push(seg);
          push(this.outlines, this.sectors[here].id, seg);
          push(this.outlines, this.sectors[there].id, seg);
        });
      }
    }
  }

  // What does not move goes onto the planet as two textures: territory (lit
  // with the ground) and lines (borders, roads, the front).
  paintTheater() {
    const S = TEX_SCALE;
    const make = () => {
      const c = document.createElement("canvas");
      c.width = 1140 * S;
      c.height = 640 * S;
      const ctx = c.getContext("2d");
      ctx.scale(S, S);
      return [c, ctx];
    };
    const [ter, tctx] = make();
    const r = HEX_R * 0.88;
    for (const hex of this.hexes) {
      const [cr, cg, cb, ca] = hex.blu ? BLU : RED;
      tctx.fillStyle = `rgba(${cr},${cg},${cb},${((ca * hex.alpha) / 255).toFixed(3)})`;
      tctx.beginPath();
      for (let i = 0; i < 6; i++) {
        const a = (-90 + 60 * i) * DEG;
        tctx[i ? "lineTo" : "moveTo"](hex.x + Math.cos(a) * r, hex.y + Math.sin(a) * r);
      }
      tctx.closePath();
      tctx.fill();
    }
    const [lines, lctx] = make();
    lctx.lineCap = "round";
    const stroke = (segs, width, colour) => {
      lctx.strokeStyle = colour;
      lctx.lineWidth = width * TO_THEATER;
      lctx.beginPath();
      for (const [ax, ay, bx, by] of segs) {
        lctx.moveTo(ax, ay);
        lctx.lineTo(bx, by);
      }
      lctx.stroke();
    };
    stroke(this.borders, 1.4, "rgba(20,14,10,0.51)");
    for (const l of this.links) {
      const a = this.byId.get(l.a), b = this.byId.get(l.b);
      if (!a || !b) continue;
      const front = l.side === "front";
      const steps = 16;
      const all = [], odd = [];
      for (let i = 1; i <= steps; i++) {
        const t0 = (i - 1) / steps, t1 = i / steps;
        const seg = [a.x + (b.x - a.x) * t0, a.y + (b.y - a.y) * t0, a.x + (b.x - a.x) * t1, a.y + (b.y - a.y) * t1];
        all.push(seg);
        if (!front || i & 1) odd.push(seg);
      }
      stroke(all, 5, "rgba(14,10,8,0.67)");
      stroke(odd, 2.4, front ? GOLD : l.side === "blu" ? BLU_ROAD : RED_ROAD);
    }
    stroke(this.frontSegs, 14, "rgba(255,214,130,0.13)");
    stroke(this.frontSegs, 6, "rgba(18,12,8,0.82)");
    stroke(this.frontSegs, 3, "rgb(255,240,210)");
    this.upload("territory", ter, 0, 0, true);
    this.upload("lines", lines, 0, 0, true);
  }

  // Sector markers: HTML, so their names stay crisp; they ride the planet.
  buildMarkers() {
    this.markers.replaceChildren();
    this.markerEls.clear();
    for (const s of this.sectors) {
      const el = document.createElement("button");
      el.type = "button";
      el.className = "sector";
      el.setAttribute("aria-label", s.name);
      const mode = (s.mode || "").toUpperCase().replace("PAYLOAD", "PL");
      el.innerHTML =
        (s.attacker ? '<span class="pulse"></span>' : "") +
        `<span class="sec-card"><span class="mode"></span></span>` +
        (s.hq ? `<span class="emblem">${s.owner === "red" ? "R" : "B"}</span>` : "") +
        (s.attacker && s.stages ? `<span class="stage-tag ${s.attacker}">${s.stage}/${s.stages}</span>` : "") +
        `<span class="plate"><span></span></span>`;
      el.querySelector(".mode").textContent = mode;
      el.querySelector(".plate span").textContent = s.name;
      el.addEventListener("click", () => {
        if (!this.dragged) this.select(s.id);
      });
      el.addEventListener("pointerenter", () => this.setHover(s.id));
      el.addEventListener("pointerleave", () => this.setHover(""));
      this.markers.append(el);
      this.markerEls.set(s.id, el);
    }
    this.updateMarkerClasses();
  }

  updateMarkerClasses() {
    for (const s of this.sectors) {
      const el = this.markerEls.get(s.id);
      if (!el) continue;
      el.className = ["sector", s.owner, s.hq && "hq", s.attacker && "target", s.id === this.selected && "selected", s.id === this.hover && "hovered"]
        .filter(Boolean)
        .join(" ");
    }
  }

  placeMarkers() {
    // Markers keep their size relative to the planet (the game's are sized
    // for a planet about 460 px across); small ones drop most names.
    const scale = Math.min(1.15, Math.max(0.55, this.R / 330));
    this.markers.classList.toggle("compact", scale < 0.8);
    for (const s of this.sectors) {
      const el = this.markerEls.get(s.id);
      if (!el) continue;
      const p = this.project(s.x, s.y);
      const shown = p.z > 0.18;
      el.style.transform = `translate(${Math.round(p.x)}px, ${Math.round(p.y)}px) scale(${scale.toFixed(3)})`;
      el.style.visibility = shown ? "visible" : "hidden";
    }
  }

  // Input -------------------------------------------------------------------

  bindInput() {
    const root = this.root;
    const local = (e) => {
      const r = root.getBoundingClientRect();
      return [e.clientX - r.left, e.clientY - r.top];
    };
    root.addEventListener("pointerdown", (e) => {
      if (e.button !== 0 || e.target.closest(".planet-zoom")) return;
      const [x, y] = local(e);
      this.drag = { x, y, lon: this.lon, lat: this.lat, at: this.now(), id: e.pointerId };
      this.spinLon = this.spinLat = 0;
      this.dragging = true;
      this.dragged = false;
    });
    root.addEventListener("pointermove", (e) => {
      const [x, y] = local(e);
      if (this.dragging && this.drag && e.pointerId === this.drag.id) {
        const dx = x - this.drag.x, dy = y - this.drag.y;
        if (!this.dragged && Math.abs(dx) + Math.abs(dy) > 5) {
          this.dragged = true;
          root.setPointerCapture(e.pointerId);
          root.classList.add("dragging");
        }
        if (!this.dragged) return;
        const lon = this.drag.lon - dx / this.R / DEG;
        const lat = Math.min(75, Math.max(-75, this.drag.lat + dy / this.R / DEG));
        const now = this.now();
        const dt = Math.max(0.001, now - this.drag.at);
        this.spinLon = 0.7 * this.spinLon + (0.3 * (lon - this.lon)) / dt;
        this.spinLat = 0.7 * this.spinLat + (0.3 * (lat - this.lat)) / dt;
        this.drag.at = now;
        this.lon = this.targetLon = lon;
        this.lat = this.targetLat = lat;
        this.idleSince = now;
        this.kick();
      } else if (e.target === this.markers || e.target === this.front || e.target === root) {
        this.setHover(this.sectorAt(x, y));
      }
    });
    const end = (e) => {
      if (!this.dragging) return;
      this.dragging = false;
      root.classList.remove("dragging");
      if (this.now() - (this.drag ? this.drag.at : 0) > 0.12) this.spinLon = this.spinLat = 0;
      if (!this.dragged && e.type === "pointerup" && !e.target.closest(".sector")) {
        const [x, y] = local(e);
        const id = this.sectorAt(x, y);
        if (id) this.select(id);
      }
      // Clicks fire after pointerup: let a marker's click see the drag first.
      setTimeout(() => (this.dragged = false), 0);
      this.kick();
    };
    root.addEventListener("pointerup", end);
    root.addEventListener("pointercancel", end);
    root.addEventListener("pointerleave", () => {
      if (!this.dragging) this.setHover("");
    });

    const zoom = document.createElement("div");
    zoom.className = "planet-zoom";
    zoom.innerHTML = '<button type="button" aria-label="Zoom in">+</button><button type="button" aria-label="Zoom out">−</button>';
    const [zin, zout] = zoom.querySelectorAll("button");
    zin.addEventListener("click", () => this.setZoom(this.zoom * 1.15));
    zout.addEventListener("click", () => this.setZoom(this.zoom / 1.15));
    root.append(zoom);
  }

  setZoom(z) {
    this.zoom = Math.min(1.9, Math.max(0.8, z));
    this.idleSince = this.now();
    this.kick();
  }

  // Frames ------------------------------------------------------------------

  kick() {
    if (this.frame || !this.visible || document.hidden) return;
    this.frame = requestAnimationFrame(() => {
      this.frame = 0;
      this.render();
      // Something always moves (stars, clouds, chevrons) unless motion is off.
      if (!reduceMotion || this.dragging || Math.abs(this.spinLon) + Math.abs(this.spinLat) > 0.5 || this.settling) this.kick();
    });
  }

  animate() {
    const now = this.now();
    const dt = Math.min(0.1, Math.max(0, now - this.last));
    this.last = now;
    this.settling = false;
    if (!this.dragging) {
      if (Math.abs(this.spinLon) + Math.abs(this.spinLat) > 0.5) {
        this.lon += this.spinLon * dt;
        this.lat = Math.min(75, Math.max(-75, this.lat + this.spinLat * dt));
        const drag = Math.exp(-dt * 2.2);
        this.spinLon *= drag;
        this.spinLat *= drag;
        this.targetLon = this.lon;
        this.targetLat = this.lat;
        this.idleSince = now;
      } else {
        this.spinLon = this.spinLat = 0;
        const k = Math.min(1, dt * 3);
        this.lon += (this.targetLon - this.lon) * k;
        this.lat += (this.targetLat - this.lat) * k;
        this.settling = Math.abs(this.targetLon - this.lon) + Math.abs(this.targetLat - this.lat) > 0.05;
      }
    }
    if (!reduceMotion) {
      const idle = Math.min(1, Math.max(0, (now - this.idleSince - 4) / 6));
      this.sway = idle * 7 * Math.sin(now * 0.07);
    }
  }

  render() {
    if (this.w < 1 || this.h < 1) return;
    this.animate();
    this.fit();
    const t = this.now();
    this.drawBack(t);
    this.drawGlobe(t);
    this.drawFront(t);
    this.placeMarkers();
  }

  drawBack(t) {
    const ctx = this.back.getContext("2d");
    const { w, h, dpr, cx, cy, R } = this;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    const sky = ctx.createRadialGradient(w * 0.5, h * 0.55, 0, w * 0.5, h * 0.55, Math.hypot(w, h) * 0.6);
    sky.addColorStop(0, "#2a1a12");
    sky.addColorStop(0.45, "#120c09");
    sky.addColorStop(1, "#050403");
    ctx.fillStyle = sky;
    ctx.fillRect(0, 0, w, h);
    for (const s of this.stars) {
      const twinkle = reduceMotion ? 0.8 : 0.55 + 0.45 * Math.sin(t * 0.8 + s.phase);
      ctx.fillStyle = `rgba(255,240,220,${((150 * twinkle) / 255).toFixed(3)})`;
      ctx.beginPath();
      ctx.arc(s.x * w, s.y * h, s.size, 0, Math.PI * 2);
      ctx.fill();
    }
    // Glow behind the planet.
    const glow = ctx.createRadialGradient(cx, cy, R * 0.98, cx, cy, R * 1.35);
    glow.addColorStop(0, "rgba(255,190,110,0.27)");
    glow.addColorStop(1, "rgba(255,190,110,0)");
    ctx.fillStyle = glow;
    ctx.beginPath();
    ctx.arc(cx, cy, R * 1.35, 0, Math.PI * 2);
    ctx.fill();
  }

  drawGlobe(t) {
    const gl = this.gl;
    const { dpr } = this;
    gl.viewport(0, 0, this.glCanvas.width, this.glCanvas.height);
    gl.clearColor(0, 0, 0, 0);
    gl.clear(gl.COLOR_BUFFER_BIT);
    const ready = this.readyAt ? Math.min(1, (t - this.readyAt) / 0.6) : 0;
    gl.uniform2f(this.u.uSize, this.glCanvas.width, this.glCanvas.height);
    gl.uniform3f(this.u.uView, this.cx * dpr, this.cy * dpr, this.R * dpr);
    gl.uniform2f(this.u.uRot, this.lon + this.sway, this.lat);
    gl.uniform1f(this.u.uCloud, reduceMotion ? 0 : (t * 0.8) % 360);
    gl.uniform1f(this.u.uReady, ready);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    if (ready < 1) this.settling = true;
  }

  // What moves: the targets pulse, the selection and hover glow, arrows flow;
  // then the atmosphere's edge over all of it.
  drawFront(t) {
    const ctx = this.front.getContext("2d");
    const { w, h, dpr, cx, cy, R } = this;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);
    const r = HEX_R * 0.88;
    const hexAt = (hex, fill) => {
      const c = this.project(hex.x, hex.y);
      if (c.z < 0.02) return;
      ctx.globalAlpha = Math.min(1, c.z * 4);
      ctx.fillStyle = fill;
      ctx.beginPath();
      for (let i = 0; i < 6; i++) {
        const a = (-90 + 60 * i) * DEG;
        const p = this.project(hex.x + Math.cos(a) * r, hex.y + Math.sin(a) * r);
        ctx[i ? "lineTo" : "moveTo"](p.x, p.y);
      }
      ctx.closePath();
      ctx.fill();
    };
    const pulse = reduceMotion ? 0.5 : 0.5 + 0.5 * Math.sin(t * 3.2);
    for (const s of this.sectors) {
      if (!s.attacker) continue;
      const [cr, cg, cb] = s.attacker === "blu" ? BLU_ARROW : RED_ARROW;
      const fill = `rgba(${cr},${cg},${cb},${((26 + 46 * pulse) / 255).toFixed(3)})`;
      for (const i of this.regions.get(s.id) || []) hexAt(this.hexes[i], fill);
    }
    if (this.hover && this.hover !== this.selected) {
      for (const i of this.regions.get(this.hover) || []) hexAt(this.hexes[i], "rgba(255,255,255,0.1)");
    }
    if (this.selected) {
      for (const i of this.regions.get(this.selected) || []) hexAt(this.hexes[i], "rgba(255,240,200,0.16)");
      ctx.globalAlpha = 1;
      ctx.strokeStyle = GOLD;
      ctx.lineCap = "round";
      ctx.lineWidth = (3 * R) / 300;
      ctx.beginPath();
      for (const [ax, ay, bx, by] of this.outlines.get(this.selected) || []) {
        const a = this.project(ax, ay), b = this.project(bx, by);
        if (a.z > 0.02 && b.z > 0.02) {
          ctx.moveTo(a.x, a.y);
          ctx.lineTo(b.x, b.y);
        }
      }
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
    for (const s of this.sectors) {
      const from = s.from ? this.byId.get(s.from) : null;
      if (from && s.attacker) this.arrow(ctx, from, s, Math.max(1, s.stage || 0), s.attacker === "blu" ? BLU_ARROW : RED_ARROW, t);
    }

    // The atmosphere's edge.
    const rim = ctx.createRadialGradient(cx, cy, R * 0.82, cx, cy, R);
    rim.addColorStop(0, "rgba(10,6,4,0)");
    rim.addColorStop(1, "rgba(10,6,4,0.59)");
    ctx.fillStyle = rim;
    ctx.beginPath();
    ctx.arc(cx, cy, R, 0, Math.PI * 2);
    ctx.fill();
    const edge = ctx.createRadialGradient(cx, cy, R * 0.985, cx, cy, R * 1.012);
    edge.addColorStop(0, "rgba(255,214,150,0.27)");
    edge.addColorStop(1, "rgba(255,214,150,0)");
    ctx.fillStyle = edge;
    ctx.beginPath();
    ctx.arc(cx, cy, R * 1.012, 0, Math.PI * 2);
    ctx.arc(cx, cy, R * 0.985, 0, Math.PI * 2, true);
    ctx.fill();
  }

  arrow(ctx, from, to, stage, colour, t) {
    // A bowed path in the theater plane, stopping short of both markers.
    let dx = to.x - from.x, dy = to.y - from.y;
    const len = Math.hypot(dx, dy);
    if (len < 100) return;
    dx /= len;
    dy /= len;
    const ax = from.x + dx * 44, ay = from.y + dy * 44, bx = to.x - dx * 58, by = to.y - dy * 58;
    const qx = (ax + bx) * 0.5 - dy * len * 0.3, qy = (ay + by) * 0.5 + dx * len * 0.3;
    const at = (u) => {
      const v = 1 - u;
      return this.project(v * v * ax + 2 * v * u * qx + u * u * bx, v * v * ay + 2 * v * u * qy + u * u * by);
    };
    const steps = 24;
    const pts = [];
    for (let i = 0; i <= steps; i++) pts.push(at(i / steps));
    if (pts.some((p) => p.z < 0.05)) return; // round the far side: leave it out
    const k = this.R / 300;
    const w = (5 + 2.2 * stage) * k;
    const strip = (extra, fill) => {
      const left = [], right = [];
      for (let i = 0; i <= steps; i++) {
        const a = pts[Math.max(0, i - 1)], b = pts[Math.min(steps, i + 1)];
        let tx = b.x - a.x, ty = b.y - a.y;
        const tl = Math.hypot(tx, ty) || 1;
        tx /= tl;
        ty /= tl;
        const half = (w * (0.55 + (0.45 * i) / steps) + extra) * 0.5;
        left.push([pts[i].x - ty * half, pts[i].y + tx * half]);
        right.push([pts[i].x + ty * half, pts[i].y - tx * half]);
      }
      ctx.fillStyle = fill;
      ctx.beginPath();
      left.forEach(([x, y], i) => ctx[i ? "lineTo" : "moveTo"](x, y));
      for (let i = right.length - 1; i >= 0; i--) ctx.lineTo(right[i][0], right[i][1]);
      ctx.closePath();
      ctx.fill();
      const end = pts[steps], before = pts[steps - 1];
      let tx = end.x - before.x, ty = end.y - before.y;
      const tl = Math.hypot(tx, ty) || 1;
      tx /= tl;
      ty /= tl;
      const hw = w * 1.25 + extra * 0.7, hl = w * 1.6 + extra * 0.6, back = extra * 0.35;
      ctx.beginPath();
      ctx.moveTo(end.x - tx * back - ty * hw, end.y - ty * back + tx * hw);
      ctx.lineTo(end.x - tx * back + ty * hw, end.y - ty * back - tx * hw);
      ctx.lineTo(end.x + tx * hl, end.y + ty * hl);
      ctx.closePath();
      ctx.fill();
    };
    strip(4 * k, "rgba(0,0,0,0.59)");
    strip(0, `rgb(${colour.join(",")})`);

    ctx.lineCap = "round";
    ctx.lineWidth = 1.6 * k;
    for (let i = 0; i < 4; i++) {
      const u = (((reduceMotion ? 0 : t * 0.32) + i / 4) % 1) * 0.92;
      const p = at(u), q = at(Math.min(1, u + 0.02));
      let tx = q.x - p.x, ty = q.y - p.y;
      const tl = Math.hypot(tx, ty) || 1;
      tx /= tl;
      ty /= tl;
      const fade = Math.sin((u / 0.92) * Math.PI);
      const s = w * (0.55 + 0.45 * u) * 0.36;
      ctx.strokeStyle = `rgba(255,246,226,${((170 * fade) / 255).toFixed(3)})`;
      ctx.beginPath();
      ctx.moveTo(p.x - tx * s - ty * s, p.y - ty * s + tx * s);
      ctx.lineTo(p.x, p.y);
      ctx.lineTo(p.x - tx * s + ty * s, p.y - ty * s - tx * s);
      ctx.stroke();
    }
  }
}
