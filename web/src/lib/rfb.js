// A minimal RFB (VNC) client over WebSocket, framework-agnostic: give it a canvas
// and a WebSocket URL and it renders the box's desktop and forwards input. It
// speaks just enough of the protocol — no-auth handshake, a forced 32-bpp pixel
// format so blitting is a byte reorder, Raw and CopyRect updates, pointer and
// keyboard — the same subset the standalone desktop page uses. connectDesktop
// returns a handle with close(); call it to tear down the socket and listeners.

const SPECIAL = {
  Backspace: 0xff08, Tab: 0xff09, Enter: 0xff0d, Escape: 0xff1b, Delete: 0xffff,
  Home: 0xff50, End: 0xff57, PageUp: 0xff55, PageDown: 0xff56, Insert: 0xff63,
  ArrowLeft: 0xff51, ArrowUp: 0xff52, ArrowRight: 0xff53, ArrowDown: 0xff54,
  Shift: 0xffe1, Control: 0xffe3, Alt: 0xffe9, Meta: 0xffeb, CapsLock: 0xffe5,
  F1: 0xffbe, F2: 0xffbf, F3: 0xffc0, F4: 0xffc1, F5: 0xffc2, F6: 0xffc3,
  F7: 0xffc4, F8: 0xffc5, F9: 0xffc6, F10: 0xffc7, F11: 0xffc8, F12: 0xffc9
};

export function connectDesktop(canvas, wsUrl, { onStatus = () => {}, onName = () => {}, inputOnly = false, inputTarget = null } = {}) {
  // In input-only mode there is no canvas: the pixels arrive as a separate video
  // stream, and this connection exists purely to inject pointer/keyboard events
  // through the VNC server. Input listeners then attach to inputTarget (the video
  // element) and coordinates scale against the framebuffer size from ServerInit.
  const ctx = canvas ? canvas.getContext('2d', { alpha: false }) : null;
  const target = inputTarget || canvas;
  let fbW = canvas ? canvas.width : 0;
  let fbH = canvas ? canvas.height : 0;
  const ws = new WebSocket(wsUrl);
  ws.binaryType = 'arraybuffer';

  // --- byte reader over incoming WebSocket chunks ---
  let inbuf = new Uint8Array(0);
  let waiter = null;
  function feed(chunk) {
    const merged = new Uint8Array(inbuf.length + chunk.length);
    merged.set(inbuf); merged.set(chunk, inbuf.length);
    inbuf = merged;
    if (waiter && inbuf.length >= waiter.n) {
      const { n, resolve } = waiter; waiter = null;
      const out = inbuf.slice(0, n); inbuf = inbuf.slice(n); resolve(out);
    }
  }
  function readExactly(n) {
    if (inbuf.length >= n) { const out = inbuf.slice(0, n); inbuf = inbuf.slice(n); return Promise.resolve(out); }
    return new Promise((resolve) => { waiter = { n, resolve }; });
  }
  const u16 = (b) => (b[0] << 8) | b[1];
  const u32 = (b) => ((b[0] << 24) | (b[1] << 16) | (b[2] << 8) | b[3]) >>> 0;

  function send(bytes) { if (ws.readyState === 1) ws.send(bytes); }
  function requestUpdate(incremental) {
    const m = new Uint8Array(10), dv = new DataView(m.buffer);
    m[0] = 3; m[1] = incremental ? 1 : 0;
    dv.setUint16(6, fbW); dv.setUint16(8, fbH);
    send(m);
  }

  async function run() {
    await readExactly(12);
    send(new TextEncoder().encode('RFB 003.008\n'));

    const count = (await readExactly(1))[0];
    if (count === 0) { const rl = u32(await readExactly(4)); throw new Error('rejected: ' + new TextDecoder().decode(await readExactly(rl))); }
    const types = await readExactly(count);
    if (![...types].includes(1)) throw new Error('server requires authentication (unsupported)');
    send(new Uint8Array([1]));
    if (u32(await readExactly(4)) !== 0) throw new Error('security handshake failed');

    send(new Uint8Array([1]));
    const si = await readExactly(24);
    const w = u16(si.subarray(0, 2)), h = u16(si.subarray(2, 4));
    const nameLen = u32(si.subarray(20, 24));
    const name = new TextDecoder().decode(await readExactly(nameLen));
    fbW = w; fbH = h;
    if (canvas) { canvas.width = w; canvas.height = h; ctx.fillStyle = '#000'; ctx.fillRect(0, 0, w, h); }
    onName(name); onStatus(w + '×' + h, 'up');

    // Input-only: the framebuffer size is all we needed. Don't ask for pixels —
    // a separate video stream carries those — just leave input wired and stop.
    if (inputOnly) { if (target && target.focus) target.focus(); return; }

    send(new Uint8Array([
      0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0
    ])); // SetPixelFormat: 32-bpp true colour, R<<16 G<<8 B<<0 -> pixel bytes [B,G,R,x]

    const enc = new Uint8Array(12), edv = new DataView(enc.buffer);
    enc[0] = 2; edv.setUint16(2, 2); edv.setInt32(4, 0); edv.setInt32(8, 1); // Raw, CopyRect
    send(enc);

    requestUpdate(false);
    if (target && target.focus) target.focus();

    while (true) {
      const type = (await readExactly(1))[0];
      if (type === 0) await framebufferUpdate();
      else if (type === 1) { const hd = await readExactly(5); await readExactly(u16(hd.subarray(3, 5)) * 6); }
      else if (type === 2) { /* bell */ }
      else if (type === 3) { const hd = await readExactly(7); await readExactly(u32(hd.subarray(3, 7))); }
      else throw new Error('unknown server message ' + type);
    }
  }

  async function framebufferUpdate() {
    await readExactly(1);
    const nrects = u16(await readExactly(2));
    for (let i = 0; i < nrects; i++) {
      const r = await readExactly(12), dv = new DataView(r.buffer, r.byteOffset, r.byteLength);
      const x = dv.getUint16(0), y = dv.getUint16(2), w = dv.getUint16(4), h = dv.getUint16(6);
      const encoding = dv.getInt32(8);
      if (encoding === 0) {
        if (w === 0 || h === 0) continue;
        const px = await readExactly(w * h * 4);
        const img = ctx.createImageData(w, h), o = img.data;
        for (let p = 0; p < w * h; p++) {
          o[p * 4] = px[p * 4 + 2]; o[p * 4 + 1] = px[p * 4 + 1]; o[p * 4 + 2] = px[p * 4]; o[p * 4 + 3] = 255;
        }
        ctx.putImageData(img, x, y);
      } else if (encoding === 1) {
        const s = await readExactly(4), sdv = new DataView(s.buffer, s.byteOffset, s.byteLength);
        ctx.drawImage(canvas, sdv.getUint16(0), sdv.getUint16(2), w, h, x, y, w, h);
      } else {
        throw new Error('unsupported encoding ' + encoding);
      }
    }
    requestUpdate(true);
  }

  // --- input ---
  let buttons = 0;
  function pointer(e) {
    if (!fbW || !fbH) return;
    const r = target.getBoundingClientRect();
    // The video/canvas is letterboxed (object-fit: contain), so map from the
    // displayed content box, not the element box, or the cursor drifts.
    const scale = Math.min(r.width / fbW, r.height / fbH);
    const dw = fbW * scale, dh = fbH * scale;
    const ox = r.left + (r.width - dw) / 2, oy = r.top + (r.height - dh) / 2;
    const x = Math.max(0, Math.min(fbW - 1, Math.round((e.clientX - ox) / scale)));
    const y = Math.max(0, Math.min(fbH - 1, Math.round((e.clientY - oy) / scale)));
    const m = new Uint8Array(6), dv = new DataView(m.buffer);
    m[0] = 5; m[1] = buttons; dv.setUint16(2, x); dv.setUint16(4, y);
    send(m);
  }
  function keysym(e) {
    if (e.key in SPECIAL) return SPECIAL[e.key];
    if (e.key.length === 1) { const c = e.key.codePointAt(0); return c < 0x100 ? c : 0x01000000 + c; }
    return 0;
  }
  function key(e, down) {
    const ks = keysym(e);
    if (!ks) return;
    const m = new Uint8Array(8), dv = new DataView(m.buffer);
    m[0] = 4; m[1] = down ? 1 : 0; dv.setUint32(4, ks >>> 0);
    send(m); e.preventDefault();
  }

  const onMouseDown = (e) => { if (target.focus) target.focus(); buttons |= 1 << e.button; pointer(e); e.preventDefault(); };
  const onMouseUp = (e) => { buttons &= ~(1 << e.button); pointer(e); e.preventDefault(); };
  const onMouseMove = (e) => pointer(e);
  const onContext = (e) => e.preventDefault();
  const onWheel = (e) => {
    const bit = e.deltaY < 0 ? 3 : 4;
    buttons |= 1 << bit; pointer(e); buttons &= ~(1 << bit); pointer(e); e.preventDefault();
  };
  const onKeyDown = (e) => key(e, true);
  const onKeyUp = (e) => key(e, false);

  target.addEventListener('mousedown', onMouseDown);
  target.addEventListener('mouseup', onMouseUp);
  target.addEventListener('mousemove', onMouseMove);
  target.addEventListener('contextmenu', onContext);
  target.addEventListener('wheel', onWheel, { passive: false });
  target.addEventListener('keydown', onKeyDown);
  target.addEventListener('keyup', onKeyUp);

  ws.onmessage = (ev) => feed(new Uint8Array(ev.data));
  ws.onopen = () => { onStatus('connecting…'); run().catch((err) => { onStatus(String(err.message || err), 'err'); ws.close(); }); };
  ws.onclose = () => onStatus('disconnected', 'err');
  ws.onerror = () => onStatus('connection error', 'err');

  return {
    close() {
      try { ws.close(); } catch { /* already closed */ }
      target.removeEventListener('mousedown', onMouseDown);
      target.removeEventListener('mouseup', onMouseUp);
      target.removeEventListener('mousemove', onMouseMove);
      target.removeEventListener('contextmenu', onContext);
      target.removeEventListener('wheel', onWheel);
      target.removeEventListener('keydown', onKeyDown);
      target.removeEventListener('keyup', onKeyUp);
    }
  };
}
