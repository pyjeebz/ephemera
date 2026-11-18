// Falling sand for the ephemera landing page.
//
// Every box on the page is a grid of cells. A cell is empty, solid, or holds
// one grain with one of six sand tones. Each step, grains fall straight down
// if they can, otherwise slide diagonally down to a random side first, and
// otherwise stay put. Rows are processed bottom up, each in a random
// direction, so piles don't lean. Grids are drawn at their own resolution and
// scaled up with square pixels.

const CELL = 4; // CSS pixels per cell for the full-width layers
const STEP_MS = 1000 / 60;
const TONES = ['#F3DCAE', '#E9C88F', '#DEB577', '#D0A262', '#BF8D4E', '#A67840'];
const UNTRUSTED = 7; // a grain that tries to phone home
const FLASH = 8; // a blocked connection hitting the wall
const WALL = 9; // lane walls between agents
const SPAWN = 4; // box rows hidden under the corner labels
const reduceMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

// Palette as little-endian RGBA words for ImageData.
const PAL = new Uint32Array(10);
const word = (hex) => {
  const n = parseInt(hex.slice(1), 16);
  return (0xff << 24) | ((n & 0xff) << 16) | (n & 0xff00) | (n >> 16);
};
TONES.forEach((t, i) => { PAL[i + 1] = word(t); });
PAL[UNTRUSTED] = word('#C4633F');
PAL[FLASH] = word('#FFE7B8');
PAL[WALL] = word('#2C2C2C');

const tone = () => 1 + ((Math.random() * 6) | 0);
const rand = (a, b) => a + Math.random() * (b - a);
const randi = (a, b) => (a + Math.random() * (b - a + 1)) | 0;

class Grid {
  constructor(w, h) {
    this.w = w;
    this.h = h;
    this.c = new Uint8Array(w * h);
    this.solid = new Uint8Array(w * h);
    this.drain = new Uint8Array(w); // bottom-row cells that are open
    this.count = 0;
  }

  add(x, y, t) {
    x |= 0; y |= 0;
    if (x < 0 || x >= this.w || y < 0 || y >= this.h) return false;
    const i = y * this.w + x;
    if (this.c[i] || this.solid[i]) return false;
    this.c[i] = t;
    this.count++;
    return true;
  }

  take(i) {
    const t = this.c[i];
    if (t) { this.c[i] = 0; this.count--; }
    return t;
  }

  clear() {
    this.c.fill(0);
    this.drain.fill(0);
    this.count = 0;
  }

  step() {
    const { w, h, c, solid, drain } = this;
    const last = (h - 1) * w;
    for (let x = 0; x < w; x++) {
      if (drain[x] && c[last + x]) { c[last + x] = 0; this.count--; }
    }
    for (let y = h - 2; y >= 0; y--) {
      const row = y * w;
      const below = row + w;
      const ltr = Math.random() < 0.5;
      for (let k = 0; k < w; k++) {
        const x = ltr ? k : w - 1 - k;
        const i = row + x;
        const v = c[i];
        if (!v) continue;
        const b = below + x;
        if (!c[b] && !solid[b]) { c[b] = v; c[i] = 0; continue; }
        const d = Math.random() < 0.5 ? -1 : 1;
        let nx = x + d;
        if (nx >= 0 && nx < w && !c[below + nx] && !solid[below + nx]) { c[below + nx] = v; c[i] = 0; continue; }
        nx = x - d;
        if (nx >= 0 && nx < w && !c[below + nx] && !solid[below + nx]) { c[below + nx] = v; c[i] = 0; }
      }
    }
  }

  // Open the floor from the center out, `half` cells to each side.
  openDrain(center, half) {
    this.drain.fill(0);
    for (let x = Math.max(0, center - half); x <= Math.min(this.w - 1, center + half); x++) this.drain[x] = 1;
  }
}

class View {
  constructor(canvas, w, h) {
    this.canvas = canvas;
    canvas.width = w;
    canvas.height = h;
    this.ctx = canvas.getContext('2d');
    this.img = this.ctx.createImageData(w, h);
    this.buf = new Uint32Array(this.img.data.buffer);
  }

  draw(grid, extra) {
    const { buf } = this;
    const c = grid.c;
    for (let i = 0; i < c.length; i++) buf[i] = c[i] ? PAL[c[i]] : 0;
    if (extra) extra(buf);
    this.ctx.putImageData(this.img, 0, 0);
  }
}

// Rain falls heavier toward the sides, in slow hills, so the copy stays clear.
function rainDensity(w, base, floor = 0.05) {
  const d = new Float32Array(w);
  for (let x = 0; x < w; x++) {
    const u = x / w;
    const edge = Math.pow(Math.abs(u - 0.5) * 2, 1.6);
    const hills = 0.5 + 0.5 * Math.sin(u * Math.PI * 3.1 + 0.7);
    d[x] = base * (floor + 1.35 * edge * (0.5 + 0.5 * hills));
  }
  return d;
}

// ---------------------------------------------------------------- hero

// Mark every cell covered by an element (rect) or by its glyphs (text) as solid.
function markSolids(grid, host, els) {
  const hostRect = host.getBoundingClientRect();
  const mask = document.createElement('canvas');
  mask.width = grid.w;
  mask.height = grid.h;
  const mctx = mask.getContext('2d', { willReadFrequently: true });
  mctx.scale(1 / CELL, 1 / CELL);
  mctx.fillStyle = '#fff';
  for (const el of els) {
    if (el.tagName === 'H1') {
      const cs = getComputedStyle(el);
      mctx.font = `${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
      if ('letterSpacing' in mctx) mctx.letterSpacing = cs.letterSpacing;
      for (const word of el.querySelectorAll('span')) {
        const r = word.getBoundingClientRect();
        const m = mctx.measureText(word.textContent);
        mctx.fillText(word.textContent, r.left - hostRect.left, r.top - hostRect.top + m.fontBoundingBoxAscent);
      }
    } else {
      const r = el.getBoundingClientRect();
      mctx.fillRect(r.left - hostRect.left, r.top - hostRect.top, r.width, r.height);
    }
  }
  const px = mctx.getImageData(0, 0, grid.w, grid.h).data;
  for (let i = 0; i < grid.w * grid.h; i++) grid.solid[i] = px[i * 4 + 3] > 90 ? 1 : 0;
}

function splitWords(el) {
  if (el.dataset.split) return;
  const words = el.textContent.trim().split(/\s+/);
  el.textContent = '';
  words.forEach((w, i) => {
    const s = document.createElement('span');
    s.textContent = w;
    el.appendChild(s);
    if (i < words.length - 1) el.appendChild(document.createTextNode(' '));
  });
  el.dataset.split = '1';
}

function heroScene() {
  const host = document.getElementById('hero');
  const canvas = document.getElementById('hero-sand');
  if (!host || !canvas) return null;
  splitWords(document.getElementById('hero-title'));

  let grid, view, solids, density, phase, t, released = 0;
  const PHASES = { pour: 16 * 60, run: 3 * 60, drain: 10 * 60, empty: 1.5 * 60 };

  function setPhase(p) {
    phase = p;
    t = 0;
    released = 0;
    if (grid && p !== 'drain') grid.drain.fill(0);
  }

  function build() {
    const r = host.getBoundingClientRect();
    const w = Math.ceil(r.width / CELL);
    const h = Math.ceil(r.height / CELL);
    grid = new Grid(w, h);
    view = new View(canvas, w, h);
    markSolids(grid, host, host.querySelectorAll('.solid'));
    solids = grid.solid.slice();
    density = rainDensity(w, 0.022, 0.02);
    setPhase('pour');
  }

  function tick() {
    t++;
    // The rain never stops; the phases are what the floor and ledges do.
    for (let x = 0; x < grid.w; x++) if (Math.random() < density[x]) grid.add(x, 0, tone());
    if (phase === 'pour') {
      if (t > PHASES.pour) setPhase('run');
    } else if (phase === 'run') {
      if (t > PHASES.run) setPhase('drain');
    } else if (phase === 'drain') {
      // The floor opens from the middle out; a moment later the letters
      // and buttons let go of what they caught.
      const mid = grid.w >> 1;
      grid.openDrain(mid, Math.min(grid.w, (t / (3 * 60)) * (grid.w / 2)) | 0);
      if (t > 50) {
        // Release the letters and buttons column by column, from the center out.
        const r = Math.min(mid + 1, (t - 50) * 3);
        for (; released < r; released++) {
          for (const x of [mid - released - 1, mid + released]) {
            if (x < 0 || x >= grid.w) continue;
            for (let y = 0; y < grid.h; y++) grid.solid[y * grid.w + x] = 0;
          }
        }
      }
      // It's never quite empty while it rains, so move on once the floor
      // has been fully open for a while.
      if (t > PHASES.drain || (t > 5 * 60 && grid.count < grid.w * 3)) setPhase('empty');
    } else if (phase === 'empty') {
      if (t > PHASES.empty) {
        grid.solid.set(solids);
        setPhase('pour');
      }
    }
    grid.step();
  }

  build();
  return {
    el: host,
    build,
    tick,
    draw: () => view.draw(grid),
    // Reduced motion: a settled pile, drawn once.
    still() {
      for (let i = 0; i < 1400; i++) {
        for (let x = 0; x < grid.w; x++) if (Math.random() < density[x] * 0.5) grid.add(x, 0, tone());
        grid.step();
      }
      for (let i = 0; i < 300; i++) grid.step();
      setPhase('run');
      view.draw(grid);
    },
  };
}

// ----------------------------------------------- the wordmark in the footer

// The footer is its own sandbox. The ephemera wordmark is a mold: outside
// the letters is solid, so sand fills each letter from the bottom up and
// the counters stay open. You arrive to it full; it holds, drains out
// through the bottom of the letters, sits empty for a moment, and a light
// pour fills it again.
const PIT_CELL = 3;

function footerScene() {
  const pit = document.getElementById('pit');
  const canvas = document.getElementById('pit-sand');
  if (!pit || !canvas) return null;

  const MOLD = word('#1A1A1A');
  const STATES = { hold: 3 * 60, empty: 1.2 * 60 };
  let grid, view, entry, glyphCells, columns;
  let drops = []; // grains falling from the top of the footer into the letters
  let state = 'hold';
  let st = 0;

  // Draw the wordmark to fill the footer's width, and size the footer to it.
  function mold() {
    const family = getComputedStyle(document.body).getPropertyValue('--sans') || 'sans-serif';
    const pad = innerWidth < 640 ? 20 : 96;
    const probe = document.createElement('canvas').getContext('2d');
    probe.font = `700 100px ${family}`;
    if ('letterSpacing' in probe) probe.letterSpacing = '-4px';
    const m = probe.measureText('ephemera');
    const size = Math.floor(100 * (innerWidth - pad * 2) / m.width);
    const ascent = m.actualBoundingBoxAscent * size / 100;
    const descent = m.actualBoundingBoxDescent * size / 100;
    const top = Math.round(size * 0.16); // room above for the pour
    pit.style.height = `${Math.ceil(top + ascent + descent + size * 0.04)}px`;

    const w = Math.ceil(innerWidth / PIT_CELL);
    const h = Math.ceil(pit.getBoundingClientRect().height / PIT_CELL);
    const c = document.createElement('canvas');
    c.width = w;
    c.height = h;
    const ctx = c.getContext('2d', { willReadFrequently: true });
    ctx.scale(1 / PIT_CELL, 1 / PIT_CELL);
    ctx.font = `700 ${size}px ${family}`;
    if ('letterSpacing' in ctx) ctx.letterSpacing = `${-0.04 * size}px`;
    ctx.fillStyle = '#fff';
    ctx.fillText('ephemera', pad - m.actualBoundingBoxLeft * size / 100, top + ascent);
    const px = ctx.getImageData(0, 0, w, h).data;

    grid = new Grid(w, h);
    view = new View(canvas, w, h);
    glyphCells = 0;
    for (let i = 0; i < w * h; i++) {
      const inside = px[i * 4 + 3] > 120;
      grid.solid[i] = inside ? 0 : 1;
      if (inside) glyphCells++;
    }
    // The top of every piece of letter in each column, lowest first: a grain
    // goes into the lowest piece with room, so the word rises evenly, even
    // under overhangs a falling grain couldn't reach.
    entry = [];
    columns = [];
    for (let x = 0; x < w; x++) {
      const tops = [];
      for (let y = 0; y < h; y++) {
        if (!grid.solid[y * w + x] && (y === 0 || grid.solid[(y - 1) * w + x])) tops.unshift(y);
      }
      entry.push(tops);
      if (tops.length) columns.push(x);
    }
  }

  function fill() {
    for (let i = 0; i < grid.c.length; i++) grid.c[i] = grid.solid[i] ? 0 : tone();
    grid.count = glyphCells;
  }

  function deposit(x, t) {
    for (const y of entry[x]) if (grid.add(x, y, t)) return true;
    return false;
  }

  function build() {
    mold();
    fill();
    drops = [];
    state = 'hold';
    st = 0;
  }

  function tick() {
    st++;
    if (state === 'hold' && st > STATES.hold) { state = 'drain'; st = 0; }
    else if (state === 'drain') {
      // Out through the bottom of each letter, a grain per column at a time.
      const { w, h, c } = grid;
      for (let x = 0; x < w; x++) {
        if (Math.random() < 0.45) continue;
        for (let y = h - 1; y >= 0; y--) if (c[y * w + x]) { grid.take(y * w + x); break; }
      }
      if (grid.count === 0) { state = 'empty'; st = 0; }
    } else if (state === 'empty' && st > STATES.empty) { state = 'pour'; st = 0; }
    else if (state === 'pour') {
      // A light pour from the top of the footer, only over the letters.
      if (grid.count + drops.length < glyphCells) {
        for (let n = 0; n < 14; n++) {
          const x = columns[(Math.random() * columns.length) | 0];
          drops.push({ x, y: rand(-4, 0), v: rand(0.4, 1), t: tone() });
        }
      }
      if (grid.count >= glyphCells * 0.985 && !drops.length) { state = 'hold'; st = 0; }
    }
    const next = [];
    for (const d of drops) {
      d.v = Math.min(d.v + 0.1, 2.5);
      d.y += d.v;
      const tops = entry[d.x];
      if (d.y >= tops[tops.length - 1]) {
        if (!deposit(d.x, d.t)) deposit(Math.min(grid.w - 1, d.x + 1), d.t);
      }
      else next.push(d);
    }
    drops = next;
    grid.step();
  }

  function draw() {
    view.draw(grid, (buf) => {
      for (let i = 0; i < buf.length; i++) if (!buf[i] && !grid.solid[i]) buf[i] = MOLD;
      for (const d of drops) {
        const y = d.y | 0;
        if (y >= 0 && y < grid.h) buf[y * grid.w + d.x] = PAL[d.t];
      }
    });
  }

  build();
  return {
    el: pit,
    build,
    tick,
    draw,
    // Reduced motion: the wordmark, full.
    still() { view.draw(grid); },
  };
}

// ------------------------------------------------------ use-case boxes

const BW = 48;
const BH = 36;

function boxScene(canvas, script) {
  const card = canvas.closest('.box');
  const right = card.querySelector('[data-right]');
  const grid = new Grid(BW, BH);
  const view = new View(canvas, BW, BH);
  const label = (s) => { if (right.textContent !== s) right.textContent = s; };
  const s = script(grid, label);
  return {
    el: card,
    build() {},
    tick() { s.tick(); grid.step(); },
    draw: () => view.draw(grid, s.overlay),
    still() {
      for (let i = 0; i < 520; i++) { s.tick(); grid.step(); }
      view.draw(grid, s.overlay);
    },
  };
}

// 01: five agents, five lanes. Each fills, runs, drains and starts over on its own.
function agents(grid, label) {
  const walls = [9, 19, 28, 38];
  for (const x of walls) for (let y = 4; y < BH; y++) grid.solid[y * BW + x] = 1;
  const bounds = [[0, 8], [10, 18], [20, 27], [29, 37], [39, 47]];
  const lanes = bounds.map(([a, b]) => ({ a, b, mid: (a + b) >> 1, phase: 'wait', t: -randi(0, 160), amount: 0 }));
  const laneCount = (l) => {
    let n = 0;
    for (let y = 0; y < BH; y++) for (let x = l.a; x <= l.b; x++) if (grid.c[y * BW + x]) n++;
    return n;
  };
  return {
    tick() {
      let running = 0;
      for (const l of lanes) {
        l.t++;
        if (l.phase === 'wait' && l.t > 0) { l.phase = 'pour'; l.t = 0; l.amount = randi(45, 120); l.run = randi(60, 260); }
        else if (l.phase === 'pour') {
          if (l.t % 2 === 0) grid.add(l.mid + randi(-1, 1), SPAWN, tone());
          if (l.t / 2 >= l.amount) { l.phase = 'run'; l.t = 0; }
        } else if (l.phase === 'run') {
          if (l.t > l.run) { l.phase = 'drain'; l.t = 0; for (let x = l.mid - 1; x <= l.mid + 1; x++) grid.drain[x] = 1; }
        } else if (l.phase === 'drain') {
          if (l.t > 20) for (let x = l.a; x <= l.b; x++) grid.drain[x] = 1;
          if (laneCount(l) === 0) {
            for (let x = l.a; x <= l.b; x++) grid.drain[x] = 0;
            l.phase = 'wait'; l.t = -randi(30, 140);
          }
        }
        if (l.phase === 'pour' || l.phase === 'run') running++;
      }
      label(`${running} of 5 running`);
    },
    overlay(buf) {
      for (const x of walls) for (let y = 4; y < BH; y++) buf[y * BW + x] = PAL[WALL];
    },
  };
}

// 02: one burst of grains per test, a pass counter, exit 0, then it drains.
function tests(grid, label) {
  const TOTAL = 41;
  let phase = 'run', t = 0, passed = 0;
  return {
    tick() {
      t++;
      if (phase === 'run') {
        if (t % 9 === 0) {
          const x = randi(14, 33);
          const tt = tone();
          for (let k = 0; k < 7; k++) grid.add(x + randi(-1, 1), SPAWN + randi(0, 1), tt);
          passed++;
        }
        label(`${passed} passed`);
        if (passed >= TOTAL) { phase = 'done'; t = 0; }
      } else if (phase === 'done') {
        label(`${TOTAL} passed`);
        if (t > 80) { phase = 'drain'; t = 0; }
      } else if (phase === 'drain') {
        label('draining');
        grid.openDrain(BW >> 1, Math.min(BW, t / 4) | 0);
        if (grid.count === 0) { grid.drain.fill(0); phase = 'empty'; t = 0; }
      } else if (phase === 'empty') {
        label('empty');
        if (t > 60) { phase = 'run'; t = 0; passed = 0; }
      }
    },
  };
}

// 03: some grains try to climb out and phone home. The wall stops them, and
// they fall back in. Then everything drains, the bad grains with it.
function untrusted(grid, label) {
  let phase = 'pour', t = 0, blocked = 0;
  let probes = [];
  let flashes = [];
  return {
    tick() {
      t++;
      if (phase === 'pour') {
        if (t % 2 === 0) grid.add(BW / 2 + randi(-1, 1), SPAWN, Math.random() < 0.08 ? UNTRUSTED : tone());
        if (t > 300) { phase = 'run'; t = 0; }
      }
      if (phase === 'pour' || phase === 'run') {
        // Now and then an untrusted grain in the pile makes a run for the edge.
        if (probes.length < 2 && Math.random() < 0.02) {
          const found = [];
          for (let i = 0; i < grid.c.length; i++) if (grid.c[i] === UNTRUSTED) found.push(i);
          if (found.length) {
            const i = found[randi(0, found.length - 1)];
            grid.take(i);
            const side = Math.random() < 0.5 ? -1 : 1;
            probes.push({ x: i % BW, y: (i / BW) | 0, vx: side * rand(0.35, 0.7), vy: -rand(0.5, 0.9) });
          }
        }
        if (phase === 'run' && t > 200) { phase = 'drain'; t = 0; }
      }
      for (const p of probes) {
        p.x += p.vx;
        p.y += p.vy;
        if (p.x <= 0 || p.x >= BW - 1 || p.y <= 0) {
          blocked++;
          const fx = Math.max(0, Math.min(BW - 1, p.x | 0));
          const fy = Math.max(0, Math.min(BH - 1, p.y | 0));
          flashes.push({ x: fx, y: fy, t: 14 });
          grid.add(Math.max(1, Math.min(BW - 2, fx)), Math.max(0, fy), UNTRUSTED);
          p.dead = true;
        }
      }
      probes = probes.filter((p) => !p.dead);
      flashes = flashes.filter((f) => --f.t > 0);
      if (phase === 'drain') {
        grid.openDrain(BW >> 1, Math.min(BW, t / 5) | 0);
        if (grid.count === 0 && !probes.length) { grid.drain.fill(0); phase = 'empty'; t = 0; }
      } else if (phase === 'empty') {
        if (t > 70) { phase = 'pour'; t = 0; blocked = 0; }
      }
      label(phase === 'empty' ? 'disk deleted' : `${blocked} blocked`);
    },
    overlay(buf) {
      for (const p of probes) {
        const x = p.x | 0, y = p.y | 0;
        if (x >= 0 && x < BW && y >= 0 && y < BH) buf[y * BW + x] = PAL[UNTRUSTED];
      }
      for (const f of flashes) {
        // A short bright mark on the wall where it was stopped.
        for (let d = -2; d <= 2; d++) {
          const onSide = f.x <= 0 || f.x >= BW - 1;
          const x = onSide ? f.x : f.x + d;
          const y = onSide ? f.y + d : f.y;
          if (x >= 0 && x < BW && y >= 0 && y < BH) buf[y * BW + x] = PAL[FLASH];
        }
      }
    },
  };
}

// 04: job after job. Each one starts in an empty box; nothing carries over.
function ci(grid, label) {
  let job = 177, phase = 'pour', t = 0, x0 = 24, amount = 0, tones = [1, 2, 3];
  const startJob = () => {
    job++;
    x0 = randi(10, 37);
    amount = randi(70, 140);
    const a = randi(1, 4);
    tones = [a, a + 1, a + 2];
    phase = 'pour';
    t = 0;
  };
  startJob();
  return {
    tick() {
      t++;
      if (phase === 'pour') {
        grid.add(x0 + randi(-1, 1), SPAWN, tones[randi(0, 2)]);
        if (t >= amount) { phase = 'run'; t = 0; }
        label(`job #${job}`);
      } else if (phase === 'run') {
        label(`job #${job}`);
        if (t > 50) { phase = 'drain'; t = 0; }
      } else if (phase === 'drain') {
        grid.openDrain(x0, Math.min(BW, t / 3) | 0);
        label(`job #${job} · draining`);
        if (grid.count === 0) { grid.drain.fill(0); phase = 'empty'; t = 0; }
      } else if (phase === 'empty') {
        label('carried over: 0');
        if (t > 55) startJob();
      }
    },
  };
}

// ------------------------------------------------------------- driver

// Starts every sand scene on the page and returns a function that stops
// them. Safe to call again after stopping (React mounts effects twice in
// development).
export function mountSand() {
  let stopped = false;
  let raf = 0;
  let io = null;
  let resizeTimer = 0;
  let onResize = null;

  const start = () => {
    if (stopped) return;
    const scenes = [];
    const hero = heroScene();
    if (hero) scenes.push(hero);
    const scripts = { agents, tests, untrusted, ci };
    for (const c of document.querySelectorAll('canvas[data-scene]')) {
      const s = scripts[c.dataset.scene];
      if (s) scenes.push(boxScene(c, s));
    }
    const footer = footerScene();
    if (footer) scenes.push(footer);

    if (reduceMotion()) {
      for (const s of scenes) s.still();
      return;
    }

    // Only animate what's on screen.
    const visible = new Set(scenes.filter((s) => !s.el));
    io = new IntersectionObserver((entries) => {
      for (const e of entries) {
        const s = scenes.find((x) => x.el === e.target);
        if (!s) continue;
        if (e.isIntersecting) visible.add(s);
        else visible.delete(s);
      }
    }, { rootMargin: '100px' });
    for (const s of scenes) if (s.el) io.observe(s.el);

    let acc = 0;
    let last = performance.now();
    const frame = (now) => {
      acc = Math.min(acc + (now - last), 100);
      last = now;
      let stepped = false;
      while (acc >= STEP_MS) {
        for (const s of visible) s.tick();
        acc -= STEP_MS;
        stepped = true;
      }
      if (stepped) for (const s of visible) s.draw();
      raf = requestAnimationFrame(frame);
    };
    raf = requestAnimationFrame(frame);

    onResize = () => {
      clearTimeout(resizeTimer);
      resizeTimer = setTimeout(() => { for (const s of scenes) s.build(); }, 200);
    };
    addEventListener('resize', onResize);
  };

  // The hero's solid letters depend on the real font, so wait for it.
  (document.fonts ? document.fonts.ready : Promise.resolve()).then(start);

  return () => {
    stopped = true;
    cancelAnimationFrame(raf);
    clearTimeout(resizeTimer);
    if (io) io.disconnect();
    if (onResize) removeEventListener('resize', onResize);
  };
}
