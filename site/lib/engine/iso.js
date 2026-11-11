// Isometric line drawings for the features section.
//
// Each drawing is built once as SVG. On first sight its lines draw themselves
// in, strokes first and fills after so no face flashes over the line work,
// then a small loop keeps it alive: ports switch branches, packets hit the
// proxy, sandboxes pour, drain and leave a record behind. Everything is a
// pure function of time, so a frame never depends on the one before it.

const NS = 'http://www.w3.org/2000/svg';
const reduceMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;
const SAND = ['#F3DCAE', '#E9C88F', '#DEB577', '#D0A262', '#BF8D4E', '#A67840'];

const clamp = (v) => (v < 0 ? 0 : v > 1 ? 1 : v);
const seg = (t, a, b) => clamp((t - a) / (b - a));
const lerp = (a, b, p) => a + (b - a) * p;
const eo = (p) => 1 - Math.pow(1 - p, 3);
const ei = (p) => p * p * p;
const eio = (p) => (p < 0.5 ? 4 * p * p * p : 1 - Math.pow(-2 * p + 2, 3) / 2);

function shade(hex, f) {
  const n = parseInt(hex.slice(1), 16);
  const c = (v) => Math.round(v * f).toString(16).padStart(2, '0');
  return `#${c(n >> 16)}${c((n >> 8) & 255)}${c(n & 255)}`;
}

// ------------------------------------------------------------------ kit

function Kit(svg, s, ox, oy) {
  const reg = [];
  let cur = { a: 0, s: 0, d: 0.6, i: 0 };
  let on = true;

  // +x runs down-right, +y down-left, +z up.
  const P = (x, y, z = 0) => [ox + (x - y) * 0.866 * s, oy + (x + y) * 0.5 * s - z * s];
  const pts = (list) => list.map(([x, y, z]) => P(x, y, z).map((v) => v.toFixed(1)).join(',')).join(' ');
  const at = (a, stagger, d) => { cur = { a, s: stagger || 0, d: d || 0.6, i: 0 }; };

  // Every element made while registering gets a window in the intro.
  // 'd' elements draw their stroke in; 'f' elements fade.
  function mk(tag, attrs, parent, kind) {
    const e = document.createElementNS(NS, tag);
    for (const k in attrs) if (attrs[k] != null) e.setAttribute(k, attrs[k]);
    (parent || svg).appendChild(e);
    if (on && tag !== 'g') {
      const a = cur.a + cur.i * cur.s;
      cur.i++;
      const k = kind || (tag === 'text' ? 'f' : 'd');
      if (k === 'd') {
        e.setAttribute('pathLength', '1');
        e.style.strokeDasharray = '1 1';
        e.style.strokeDashoffset = '1';
        e.style.fillOpacity = '0';
      } else {
        e.style.opacity = '0';
      }
      reg.push({ e, a, b: a + cur.d, k });
    }
    return e;
  }
  const quiet = (fn) => { on = false; try { return fn(); } finally { on = true; } };

  const poly = (list, cls, parent, kind) => mk('polygon', { points: pts(list), class: cls }, parent, kind);
  const line = (list, cls, parent, kind) => mk('polyline', { points: pts(list), class: cls }, parent, kind);

  // A box: the visible faces are the front-left (+y), front-right (+x) and top.
  function slab(x, y, w, d, h, z = 0, cls = '', parent) {
    const g = mk('g', { class: `slab ${cls}` }, parent);
    poly([[x, y + d, z], [x + w, y + d, z], [x + w, y + d, z + h], [x, y + d, z + h]], 'face l', g);
    poly([[x + w, y, z], [x + w, y + d, z], [x + w, y + d, z + h], [x + w, y, z + h]], 'face r', g);
    poly([[x, y, z + h], [x + w, y, z + h], [x + w, y + d, z + h], [x, y + d, z + h]], 'face t', g);
    return g;
  }

  // A sandbox: a slab with a well sunk into its top.
  function tile(x, y, w, d, h, parent) {
    const g = slab(x, y, w, d, h, 0, 'tile', parent);
    const i = 0.55;
    poly([[x + i, y + i, h], [x + w - i, y + i, h], [x + w - i, y + d - i, h], [x + i, y + d - i, h]], 'well', g);
    return g;
  }

  // A grain, drawn as a voxel at the origin. Move it with place().
  function voxel(c, tone, parent) {
    const g = document.createElementNS(NS, 'g');
    (parent || svg).appendChild(g);
    const col = SAND[tone % SAND.length];
    const face = (list, fill) => {
      const e = document.createElementNS(NS, 'polygon');
      e.setAttribute('points', pts(list));
      e.setAttribute('fill', fill);
      g.appendChild(e);
    };
    face([[0, c, 0], [c, c, 0], [c, c, c], [0, c, c]], shade(col, 0.78));
    face([[c, 0, 0], [c, c, 0], [c, c, c], [c, 0, c]], shade(col, 0.6));
    face([[0, 0, c], [c, 0, c], [c, c, c], [0, c, c]], col);
    return g;
  }
  const [bx, by] = P(0, 0, 0);
  function place(g, x, y, z) {
    const [px, py] = P(x, y, z);
    g.setAttribute('transform', `translate(${(px - bx).toFixed(2)} ${(py - by).toFixed(2)})`);
  }

  // A pyramid of grains centered on (cx, cy), sorted back to front.
  function pyramid(cx, cy, z, c, base) {
    const out = [];
    for (let l = 0; l < base; l++) {
      const n = base - l;
      for (let i = 0; i < n; i++) for (let j = 0; j < n; j++) {
        out.push([cx - (n * c) / 2 + i * c, cy - (n * c) / 2 + j * c, z + l * c]);
      }
    }
    return out.sort((p, q) => (p[0] + p[1] + p[2]) - (q[0] + q[1] + q[2]));
  }

  function label(x, y, z, text, cls, dx = 0, dy = 0, anchor = 'middle') {
    const [px, py] = P(x, y, z);
    const t = mk('text', { x: (px + dx).toFixed(1), y: (py + dy).toFixed(1), class: `lbl ${cls || ''}`, 'text-anchor': anchor });
    t.textContent = text;
    return t;
  }

  // A faint floor grid, faded out toward its edges by a mask.
  function floor(x0, x1, y0, y1, step) {
    let d = '';
    for (let x = x0; x <= x1; x += step) d += `M${P(x, y0).join(',')}L${P(x, y1).join(',')}`;
    for (let y = y0; y <= y1; y += step) d += `M${P(x0, y).join(',')}L${P(x1, y).join(',')}`;
    return mk('path', { d, class: 'floor' }, null, 'f');
  }

  function render(t) {
    for (const r of reg) {
      if (r.k === 'd') {
        const span = r.b - r.a;
        r.e.style.strokeDashoffset = (1 - eio(seg(t, r.a, r.b))).toFixed(3);
        r.e.style.fillOpacity = eo(seg(t, r.a + span * 0.55, r.b + span * 0.7)).toFixed(3);
      } else {
        r.e.style.opacity = eo(seg(t, r.a, r.b)).toFixed(3);
      }
    }
  }
  const end = () => reg.reduce((m, r) => Math.max(m, r.b + (r.b - r.a) * 0.7), 0);
  function finish() {
    for (const r of reg) {
      r.e.style.strokeDasharray = '';
      r.e.style.strokeDashoffset = '';
      r.e.style.fillOpacity = '';
      r.e.style.opacity = '';
      r.e.removeAttribute('pathLength');
    }
  }

  return { P, at, mk, quiet, poly, line, slab, tile, voxel, place, pyramid, label, floor, render, end, finish, svg };
}

// ------------------------------------------------------------ the scenes

// Port forwarding: a laptop, two sandboxes on one port, localhost:3000
// switching between the branches.
function ports(svg) {
  const k = Kit(svg, 17, 0, 0);
  k.at(0, 0, 0.9);
  k.floor(-3, 27, -4, 9, 2);

  // Laptop: base, keyboard, trackpad, then the screen lifts into view.
  k.at(0.15, 0.08, 0.7);
  k.slab(0, 0, 7, 5, 0.35, 0, 'laptop');
  k.at(0.5, 0.04, 0.5);
  k.line([[0.8, 0.7, 0.35], [6.2, 0.7, 0.35], [6.2, 2.9, 0.35], [0.8, 2.9, 0.35], [0.8, 0.7, 0.35]], 'wire faint');
  for (const yy of [1.43, 2.17]) k.line([[0.8, yy, 0.35], [6.2, yy, 0.35]], 'wire faint');
  for (let xx = 1.7; xx < 6.2; xx += 0.9) k.line([[xx, 0.7, 0.35], [xx, 2.9, 0.35]], 'wire faint');
  k.line([[2.6, 3.4, 0.35], [4.4, 3.4, 0.35], [4.4, 4.5, 0.35], [2.6, 4.5, 0.35], [2.6, 3.4, 0.35]], 'wire faint');
  k.at(0.75, 0.1, 0.7);
  k.poly([[0.2, 0, 0.35], [6.8, 0, 0.35], [6.8, 0, 4.9], [0.2, 0, 4.9]], 'face screen');
  k.poly([[0.6, 0, 0.75], [6.4, 0, 0.75], [6.4, 0, 4.5], [0.6, 0, 4.5]], 'well');
  // A little sand mark on the screen.
  k.at(1.3, 0.03, 0.3);
  [1, 2, 3, 2, 1].forEach((hgt, i) => {
    for (let j = 0; j < hgt; j++) {
      const x0 = 2.6 + i * 0.36, z0 = 1.4 + j * 0.36;
      k.poly([[x0, 0, z0], [x0 + 0.3, 0, z0], [x0 + 0.3, 0, z0 + 0.3], [x0, 0, z0 + 0.3]], `pix t${(i + j) % 6}`, null, 'f');
    }
  });

  // The port lines run along the floor, under the sandboxes.
  k.at(1.1, 0.15, 0.6);
  const lineA = k.line([[7, 3, 0], [10, 3, 0]], 'port', null, 'f');
  const lineB = k.line([[7, 4.3, 0], [8.6, 4.3, 0], [8.6, 6.4, 0], [21, 6.4, 0], [21, 5, 0]], 'port', null, 'f');

  // Two sandboxes, one per branch.
  k.at(1.2, 0.12, 0.75);
  k.tile(10, -1, 6, 6, 0.9);
  k.tile(18, -1, 6, 6, 0.9);
  const piles = [];
  for (const [cx, n] of [[13, 0], [21, 1]]) {
    const g = k.mk('g', { class: 'pile' });
    k.quiet(() => {
      k.pyramid(cx, 2, 0.9, 0.62, 4).forEach(([x, y, z], i) => k.place(k.voxel(0.62, (i * 7 + n) % 6, g), x, y, z));
    });
    piles.push(g);
  }
  k.at(1.9, 0.12, 0.5);
  const labels = [
    k.label(13, -1, 0.9, 'feat-auth.localhost:3000', '', 0, -40),
    k.label(21, -1, 0.9, 'main.localhost:3000', '', 0, -40),
  ];
  const lines = [lineA, lineB];
  for (const p of piles) p.style.opacity = '0';

  const intro = k.end();
  return {
    k,
    loop(t) {
      // localhost:3000 points at one branch at a time.
      const half = 3.4;
      const cycle = Math.floor(t / half);
      const active = cycle % 2;
      const blend = eo(seg(t - cycle * half, 0, 0.35));
      const shown = eo(seg(t, intro - 0.9, intro - 0.2));
      piles.forEach((g, i) => {
        const was = (cycle + 1) % 2 === i;
        const now = active === i;
        const o = now ? lerp(was ? 1 : 0.28, 1, blend) : lerp(was ? 1 : 0.28, 0.28, blend);
        g.style.opacity = (o * shown).toFixed(3);
      });
      labels.forEach((l, i) => l.classList.toggle('on', active === i));
      lines.forEach((l, i) => {
        l.classList.toggle('on', active === i);
        l.style.strokeDashoffset = active === i ? (-(t * 16) % 14).toFixed(2) : '0';
      });
    },
  };
}

// No network unless you allow it: every connection leaves the sandbox
// through the proxy. The host on the allow list is reached through the
// gate; the others are there, but the line stops at the wall.
function network(svg) {
  const k = Kit(svg, 21, 0, 0);
  k.at(0, 0, 0.9);
  k.floor(-3, 21, -4, 13, 2);

  k.at(0.15, 0.1, 0.75);
  k.tile(0, 0, 6, 6, 0.9);
  const pile = k.mk('g', { class: 'pile' });
  k.quiet(() => k.pyramid(3, 3, 0.9, 0.62, 4).forEach(([x, y, z], i) => k.place(k.voxel(0.62, i % 6, pile), x, y, z)));
  pile.style.opacity = '0';

  const Z = 0.3;
  const OUT = [1.2, 3.0, 4.8]; // where each connection leaves the sandbox
  const HOST = [-0.3, 4.4, 9.1]; // the row each host sits on
  const WX = 10, WH = 1.2; // the proxy wall, kept low so the drawing reads over it
  const path = (i, x1) => [[6, OUT[i], Z], [7.6, OUT[i], Z], [7.6, HOST[i], Z], [x1, HOST[i], Z]];

  k.at(0.8, 0.12, 0.6);
  const allowed = k.line(path(0, 14), 'port on', null, 'f');
  k.line(path(1, WX), 'port', null, 'f');
  k.line(path(2, WX), 'port', null, 'f');

  // Hosts beyond the proxy: back to front.
  k.at(1.0, 0.12, 0.7);
  k.slab(WX, -1.8, 0.5, 2.9, WH, 0, 'wall');
  k.tile(14, HOST[0] - 1.6, 3.2, 3.2, 0.6);
  const fronts = [k.slab(WX, 2.0, 0.5, 9.2, WH, 0, 'wall')];
  k.tile(14, HOST[1] - 1.6, 3.2, 3.2, 0.6, null).classList.add('dimmed');
  k.tile(14, HOST[2] - 1.6, 3.2, 3.2, 0.6, null).classList.add('dimmed');

  k.at(1.7, 0.1, 0.5);
  const labels = [
    k.label(WX + 0.25, -1.8, WH, 'proxy', '', 0, -10),
    k.label(15.6, HOST[0] - 1.6, 0.6, 'registry.npmjs.org', 'on', 0, -14),
    k.label(15.6, HOST[1] - 1.6, 0.6, '169.254.169.254', 'off', 0, -14),
    k.label(15.6, HOST[2] - 1.6, 0.6, 'telemetry.example.com', 'off', 0, -14),
  ];

  const packets = k.quiet(() => [0, 1, 2].map((i) => k.voxel(0.42, i * 2, svg)));
  for (const g of packets) g.classList.add('mv');
  const marks = k.quiet(() => [1, 2].map((i) => {
    const y = HOST[i];
    const a = k.P(WX + 0.5, y - 0.3, 0.95), b = k.P(WX + 0.5, y + 0.3, 0.25);
    const c = k.P(WX + 0.5, y - 0.3, 0.25), d = k.P(WX + 0.5, y + 0.3, 0.95);
    return k.mk('path', { d: `M${a}L${b}M${c}L${d}`, class: 'mark' });
  }));
  // Paint order: packets go behind the front wall; marks and labels on top.
  for (const f of fronts) svg.appendChild(f);
  for (const t of svg.querySelectorAll('.dimmed')) svg.appendChild(t);
  for (const m of marks) svg.appendChild(m);
  for (const l of labels) svg.appendChild(l);

  // Where a packet is after travelling fraction p of a path.
  function along(pts, p) {
    const lens = [];
    let total = 0;
    for (let i = 1; i < pts.length; i++) {
      const l = Math.hypot(pts[i][0] - pts[i - 1][0], pts[i][1] - pts[i - 1][1]);
      lens.push(l);
      total += l;
    }
    let d = p * total;
    for (let i = 1; i < pts.length; i++) {
      if (d <= lens[i - 1] || i === pts.length - 1) {
        const f = lens[i - 1] ? Math.min(1, d / lens[i - 1]) : 0;
        return [lerp(pts[i - 1][0], pts[i][0], f), lerp(pts[i - 1][1], pts[i][1], f)];
      }
      d -= lens[i - 1];
    }
    return pts[pts.length - 1];
  }

  const intro = k.end();
  return {
    k,
    loop(t) {
      const live = seg(t, intro - 0.6, intro);
      pile.style.opacity = eo(seg(t, intro - 1.2, intro - 0.4)).toFixed(3);
      allowed.style.strokeDashoffset = (-(t * 16) % 14).toFixed(2);
      const pa = (t % 2.8) / 2.8;
      const [ax, ay] = along(path(0, 14.4), eio(pa));
      k.place(packets[0], ax - 0.21, ay - 0.21, Z);
      packets[0].style.opacity = (live * (pa < 0.92 ? 1 : 1 - seg(pa, 0.92, 1))).toFixed(3);
      [1, 2].forEach((i) => {
        const period = 3.1 + i * 0.7;
        const q = ((t + i * 1.3) % period) / period;
        const travel = seg(q, 0, 0.6);
        const [x, y] = along(path(i, WX + 0.25), ei(travel));
        k.place(packets[i], x - 0.21, y - 0.21, Z);
        packets[i].style.opacity = (live * (travel < 1 ? 1 : 0)).toFixed(3);
        marks[i - 1].style.opacity = (live * (q >= 0.6 ? 1 - eo(seg(q, 0.6, 0.95)) : 0)).toFixed(3);
      });
    },
  };
}

// A signed record of every sandbox: sand pours in, drains away, and a
// signed record rises out of the empty box onto a growing stack.
function records(svg) {
  const k = Kit(svg, 24, 0, 0);
  k.at(0, 0, 0.9);
  k.floor(-3, 23, -4, 9, 2);

  k.at(0.15, 0.1, 0.8);
  k.tile(0, 0, 6, 6, 0.9);
  k.at(0.8, 0.1, 0.5);
  k.label(3, 6, 0, 'sbx_6abe91d8', '', -34, 22);
  k.label(15.5, -1, 1.9, 'records', '', 0, -12);

  // A stack of records already kept, on the floor beside the sandbox.
  const CARD = { w: 4.4, d: 3.2, h: 0.2 };
  const SX = 13.3, SY = -1, GAP = 0.32, MAX = 5;
  const card = (x, y, z, parent) => {
    const g = k.slab(x, y, CARD.w, CARD.d, CARD.h, z, 'card', parent);
    k.line([[x + 0.5, y + 0.8, z + CARD.h], [x + 3.2, y + 0.8, z + CARD.h]], 'wire', g);
    k.line([[x + 0.5, y + 1.5, z + CARD.h], [x + 2.6, y + 1.5, z + CARD.h]], 'wire faint', g);
    k.line([[x + 0.5, y + 2.2, z + CARD.h], [x + 2.9, y + 2.2, z + CARD.h]], 'wire faint', g);
    k.poly([[x + 3.3, y + 1.7, z + CARD.h], [x + 3.9, y + 1.7, z + CARD.h], [x + 3.9, y + 2.5, z + CARD.h], [x + 3.3, y + 2.5, z + CARD.h]], 'stamp', g);
    return g;
  };
  k.at(1.0, 0.1, 0.6);
  const stack = [];
  for (let i = 0; i < MAX; i++) stack.push(card(SX, SY, i * GAP));

  // The grains and the record that this run will leave.
  const spots = k.pyramid(3, 3, 0.9, 0.62, 4);
  const grains = k.quiet(() => spots.map((_, i) => k.voxel(0.62, (i * 5) % 6, svg)));
  const moving = k.quiet(() => card(0, 0, 0, svg));
  for (const g of [...grains, moving]) g.classList.add('mv');
  const [ox, oy] = k.P(0, 0, 0);

  const intro = k.end();
  const PERIOD = 7.2;
  return {
    k,
    loop(t) {
      const live = t > intro - 0.4;
      const run = Math.max(0, t - intro + 0.4);
      const cycle = Math.floor(run / PERIOD);
      const q = run - cycle * PERIOD;
      const kept = live ? 1 + (cycle % MAX) : 1;
      // Once the stack is full, the old records fade away and it starts over.
      const fade = kept === MAX ? 1 - seg(q, 6.2, 7.0) : 1;
      stack.forEach((g, i) => { g.style.opacity = i < kept ? fade.toFixed(3) : '0'; });

      // 0 – 1.8 s: grains pour into the well, each one falling into place.
      // 2.6 – 3.4 s: they sink through the floor and are gone.
      grains.forEach((g, i) => {
        const [x, y, z] = spots[i];
        const a = i * 0.05;
        const fall = ei(seg(q, a, a + 0.38));
        const sink = seg(q, 2.6 + (spots.length - i) * 0.012, 3.1 + (spots.length - i) * 0.012);
        k.place(g, x, y, lerp(z + 5, z, fall) - ei(sink) * 1.4);
        g.style.opacity = live ? ((q >= a ? 1 : 0) * (1 - sink)).toFixed(3) : '0';
      });

      // 3.4 – 4.2 s: the record rises out of the empty sandbox.
      // 4.3 – 5.2 s: it moves onto the stack.
      const rise = eo(seg(q, 3.4, 4.2));
      const move = eio(seg(q, 4.3, 5.2));
      const top = kept * GAP;
      const x = lerp(0.8, SX, move), y = lerp(1.4, SY, move);
      const z = lerp(lerp(0.9, 3.6, rise), top, move);
      const [px, py] = k.P(x, y, z);
      moving.setAttribute('transform', `translate(${(px - ox).toFixed(2)} ${(py - oy).toFixed(2)})`);
      const landed = kept < MAX ? 1 - seg(q, 5.2, 5.4) : fade;
      moving.style.opacity = live ? (seg(q, 3.4, 3.7) * landed).toFixed(3) : '0';
      if (q > 5.2 && kept < MAX) stack[kept].style.opacity = '1';
    },
  };
}

// ------------------------------------------------------------- driver

const SCENES = { ports, network, records };

// Frame each drawing tightly: the viewBox is the box around everything
// except the floor grid and the moving parts, plus a margin. `top` leaves
// room above for things that fall in from overhead.
function fit(svg, pad, top) {
  let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
  for (const el of svg.children) {
    if (el.classList.contains('floor') || el.classList.contains('mv')) continue;
    const b = el.getBBox();
    if (!b.width && !b.height) continue;
    const tr = el.transform && el.transform.baseVal.consolidate();
    const dx = tr ? tr.matrix.e : 0, dy = tr ? tr.matrix.f : 0;
    x0 = Math.min(x0, b.x + dx); y0 = Math.min(y0, b.y + dy);
    x1 = Math.max(x1, b.x + b.width + dx); y1 = Math.max(y1, b.y + b.height + dy);
  }
  svg.setAttribute('viewBox', `${(x0 - pad).toFixed(0)} ${(y0 - top).toFixed(0)} ${(x1 - x0 + pad * 2).toFixed(0)} ${(y1 - y0 + pad + top).toFixed(0)}`);
}
const FRAME = { ports: [24, 24], network: [24, 24], records: [24, 90] };

// Builds every isometric drawing on the page and returns a function that
// stops their animation.
export function mountIso() {
  let stopped = false;
  let raf = 0;
  let io = null;
  const start = () => {
  if (stopped) return;
  const items = [];
  for (const svg of document.querySelectorAll('svg[data-iso]')) {
    const make = SCENES[svg.dataset.iso];
    if (!make) continue;
    svg.replaceChildren(); // a remount builds from scratch
    const s = make(svg);
    fit(svg, ...FRAME[svg.dataset.iso]);
    s.svg = svg;
    s.t0 = null;
    s.intro = s.k.end();
    s.finished = false;
    items.push(s);
    if (reduceMotion()) {
      s.k.finish();
      s.loop(s.intro + 4.6);
    } else {
      s.k.render(0);
      s.loop(0);
    }
  }
  if (reduceMotion() || !items.length) return;

  const visible = new Set();
  io = new IntersectionObserver((entries) => {
    for (const e of entries) {
      const s = items.find((x) => x.svg === e.target);
      if (!s) continue;
      if (e.isIntersecting) {
        if (s.t0 === null) s.t0 = performance.now();
        visible.add(s);
      } else {
        visible.delete(s);
      }
    }
  }, { threshold: 0.35 });
  for (const s of items) io.observe(s.svg);

  const frame = (now) => {
    for (const s of visible) {
      const t = (now - s.t0) / 1000;
      if (!s.finished) {
        s.k.render(t);
        if (t > s.intro) { s.k.finish(); s.finished = true; }
      }
      s.loop(t);
    }
    requestAnimationFrame(frame);
  };
  requestAnimationFrame(frame);
};

  // Labels are measured for the frame, so wait for the fonts.
  (document.fonts ? document.fonts.ready : Promise.resolve()).then(start);

  return () => {
    stopped = true;
    cancelAnimationFrame(raf);
    if (io) io.disconnect();
  };
}
