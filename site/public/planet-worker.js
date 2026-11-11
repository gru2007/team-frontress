// The planet's surface and clouds, generated off the main thread.
//
// A port of Planet() in src/frontress_ui/fui_warmap.cpp: the same noise, the
// same continents and colours, so the site shows the planet the game does.
// Answers { w, h, surface, clouds } (RGBA, clouds premultiplied).

const DEG = Math.PI / 180;

function hash3(x, y, z, seed) {
  let h = (Math.imul(x, 374761393) + Math.imul(y, 668265263) + Math.imul(z, 2147483647) + Math.imul(seed, 144665)) >>> 0;
  h = Math.imul(h ^ (h >>> 13), 1274126177) >>> 0;
  return ((h ^ (h >>> 16)) & 0xffffff) / 0xffffff;
}

function valueNoise(x, y, z, seed) {
  const xi = Math.floor(x), yi = Math.floor(y), zi = Math.floor(z);
  let fx = x - xi, fy = y - yi, fz = z - zi;
  fx = fx * fx * (3 - 2 * fx);
  fy = fy * fy * (3 - 2 * fy);
  fz = fz * fz * (3 - 2 * fz);
  const c00 = hash3(xi, yi, zi, seed) + (hash3(xi + 1, yi, zi, seed) - hash3(xi, yi, zi, seed)) * fx;
  const c01 = hash3(xi, yi, zi + 1, seed) + (hash3(xi + 1, yi, zi + 1, seed) - hash3(xi, yi, zi + 1, seed)) * fx;
  const c10 = hash3(xi, yi + 1, zi, seed) + (hash3(xi + 1, yi + 1, zi, seed) - hash3(xi, yi + 1, zi, seed)) * fx;
  const c11 = hash3(xi, yi + 1, zi + 1, seed) + (hash3(xi + 1, yi + 1, zi + 1, seed) - hash3(xi, yi + 1, zi + 1, seed)) * fx;
  const a = c00 + (c10 - c00) * fy;
  const b = c01 + (c11 - c01) * fy;
  return a + (b - a) * fz;
}

function fbm(x, y, z, seed, octaves) {
  let sum = 0, amp = 0.5, norm = 0;
  for (let i = 0; i < octaves; i++) {
    sum += amp * valueNoise(x, y, z, seed + i * 17);
    norm += amp;
    x *= 2.03;
    y *= 2.03;
    z *= 2.03;
    amp *= 0.5;
  }
  return sum / norm;
}

function smooth(a, b, x) {
  const t = Math.min(1, Math.max(0, (x - a) / (b - a)));
  return t * t * (3 - 2 * t);
}

function blob(lon, lat, lon0, lat0, rLon, rLat, strength) {
  let dl = Math.abs(lon - lon0);
  dl = Math.min(dl, 360 - dl);
  const d = Math.sqrt((dl / rLon) ** 2 + ((lat - lat0) / rLat) ** 2);
  return strength * (1 - smooth(0.55, 1.15, d));
}

function elevation(x, y, z, lon, lat) {
  let e = fbm(x * 1.7 + 11, y * 1.7 + 3, z * 1.7, 7, 6) * 2 - 1;
  e += blob(lon, lat, 0, 0, 80, 48, 0.62);
  e += blob(lon, lat, 150, 30, 50, 30, 0.42);
  e += blob(lon, lat, -130, -25, 45, 35, 0.4);
  return e - 0.18;
}

function mix(a, b, t) {
  t = Math.min(1, Math.max(0, t));
  return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t, a[2] + (b[2] - a[2]) * t];
}

function generate(W, H) {
  const surface = new Uint8ClampedArray(W * H * 4);
  const clouds = new Uint8ClampedArray(W * H * 4);
  const elev = new Float32Array(W * H);
  const pos = (i, j) => {
    const lat = 90 - (180 * (j + 0.5)) / H;
    const lon = -180 + (360 * (i + 0.5)) / W;
    const cl = Math.cos(lat * DEG);
    return [cl * Math.sin(lon * DEG), Math.sin(lat * DEG), cl * Math.cos(lon * DEG), lon, lat];
  };
  for (let j = 0; j < H; j++) {
    for (let i = 0; i < W; i++) {
      const [x, y, z, lon, lat] = pos(i, j);
      elev[j * W + i] = elevation(x, y, z, lon, lat);
    }
  }
  const deep = [16, 34, 44], shallow = [40, 86, 94], sand = [200, 170, 114];
  const ochre = [170, 122, 70], olive = [108, 108, 64], hills = [126, 86, 54];
  const rock = [168, 150, 124], snow = [236, 230, 216], ice = [222, 228, 232], coast = [70, 120, 120];
  for (let j = 0; j < H; j++) {
    for (let i = 0; i < W; i++) {
      const [x, y, z, , lat] = pos(i, j);
      const e = elev[j * W + i];
      let c;
      if (e < 0) {
        c = mix(deep, shallow, 1 + e / 0.35);
        c = mix(c, coast, smooth(-0.06, 0, e) * 0.5);
      } else {
        const wet = fbm(x * 3 + 5, y * 3, z * 3 - 2, 29, 4);
        c = mix(ochre, olive, smooth(0.45, 0.65, wet));
        c = mix(sand, c, smooth(0, 0.05, e));
        c = mix(c, hills, smooth(0.22, 0.4, e));
        c = mix(c, rock, smooth(0.4, 0.55, e));
        c = mix(c, snow, smooth(0.58, 0.7, e));
        const i2 = (i + W - 2) % W, j2 = Math.max(0, j - 2);
        const slope = (e - elev[j2 * W + i2]) * 9;
        const k = Math.min(1.3, Math.max(0.7, 1 + slope));
        c = [Math.min(255, c[0] * k), Math.min(255, c[1] * k), Math.min(255, c[2] * k)];
      }
      c = mix(c, ice, smooth(64, 76, Math.abs(lat) + fbm(x * 4, y * 4, z * 4, 41, 3) * 10));
      const p = (j * W + i) * 4;
      surface[p] = c[0];
      surface[p + 1] = c[1];
      surface[p + 2] = c[2];
      surface[p + 3] = 255;

      const n = fbm(x * 2.4 + 40, y * 4, z * 2.4, 53, 5);
      const a = smooth(0.52, 0.72, n) * (0.55 + 0.45 * Math.sin(Math.abs(lat) * DEG * 2)) * 230;
      clouds[p + 3] = a;
      clouds[p] = clouds[p + 1] = (250 * a) / 255;
      clouds[p + 2] = (244 * a) / 255;
    }
  }
  return { w: W, h: H, surface, clouds };
}

self.onmessage = (e) => {
  const { w = 1024, h = 512 } = e.data || {};
  const out = generate(w, h);
  self.postMessage(out, [out.surface.buffer, out.clouds.buffer]);
};
