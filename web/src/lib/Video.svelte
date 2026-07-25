<script>
  import { onMount } from 'svelte';
  import { connectDesktop } from '$lib/rfb.js';

  let { id } = $props();
  let video;
  let status = $state('connecting…');
  let statusCls = $state('');

  // ffmpeg encodes forced baseline / level 3.1, so the codec string is fixed.
  const MIME = 'video/mp4; codecs="avc1.42C01F"';

  onMount(() => {
    if (!('MediaSource' in window) || !MediaSource.isTypeSupported(MIME)) {
      status = 'H.264 playback unsupported here'; statusCls = 'err';
      return;
    }

    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    const ms = new MediaSource();
    video.src = URL.createObjectURL(ms);
    let sb, ws, session, edge;
    const queue = [];

    function pump() {
      if (!sb || sb.updating || queue.length === 0) return;
      let n = 0;
      for (const c of queue) n += c.length;
      const buf = new Uint8Array(n);
      let o = 0;
      for (const c of queue) { buf.set(c, o); o += c.length; }
      queue.length = 0;
      try {
        sb.appendBuffer(buf);
      } catch (e) {
        // Buffer full: drop everything comfortably behind the playhead, then it
        // recovers on the next keyframe.
        if (e.name === 'QuotaExceededError' && sb.buffered.length) {
          try { sb.remove(sb.buffered.start(0), Math.max(0, video.currentTime - 1)); } catch { /* */ }
        }
      }
    }

    ms.addEventListener('sourceopen', () => {
      try {
        sb = ms.addSourceBuffer(MIME);
      } catch (e) {
        status = 'codec error'; statusCls = 'err';
        return;
      }
      sb.addEventListener('updateend', pump);

      ws = new WebSocket(proto + location.host + '/v1/machines/' + id + '/video/ws');
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => { status = 'streaming'; statusCls = 'up'; };
      ws.onmessage = (ev) => { queue.push(new Uint8Array(ev.data)); pump(); };
      ws.onclose = () => { status = 'disconnected'; statusCls = 'err'; };
      ws.onerror = () => { status = 'stream error'; statusCls = 'err'; };

      // Keep close to the live edge: a software encoder plus MSE will drift, so
      // hop forward whenever the buffered end runs more than ~0.6 s ahead.
      edge = setInterval(() => {
        if (!video.buffered.length) return;
        const end = video.buffered.end(video.buffered.length - 1);
        if (end - video.currentTime > 0.6) video.currentTime = end - 0.1;
        if (video.paused) video.play().catch(() => {});
      }, 1000);
    });

    // Input still goes through the VNC server, input-only: this connection injects
    // pointer/keyboard while the video stream carries the pixels. The video element
    // is the input surface, so clicks land where you see them.
    session = connectDesktop(null, proto + location.host + '/v1/machines/' + id + '/desktop/ws', {
      inputOnly: true, inputTarget: video, onStatus: () => {}
    });

    return () => {
      if (edge) clearInterval(edge);
      if (session) session.close();
      try { ws && ws.close(); } catch { /* */ }
      try { if (ms.readyState === 'open') ms.endOfStream(); } catch { /* */ }
    };
  });
</script>

<div class="wrap">
  <div class="stage">
    <!-- svelte-ignore a11y_media_has_caption -->
    <video bind:this={video} tabindex="0" autoplay muted playsinline></video>
  </div>
  <div class="statusbar">
    <span class="dot" class:up={statusCls === 'up'} class:err={statusCls === 'err'}></span>
    <span>{status}</span>
    <span class="tag">H.264 · video</span>
  </div>
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; }
  .stage { flex: 1; display: grid; place-items: center; padding: 16px; min-height: 0; }
  video {
    max-width: 100%; max-height: 100%; object-fit: contain; background: #000;
    border: 1px solid hsl(var(--border)); border-radius: var(--radius); outline: none;
    box-shadow: 0 10px 40px rgba(0,0,0,.35);
  }
  .statusbar { display: flex; align-items: center; gap: 8px; padding: 8px 16px; border-top: 1px solid hsl(var(--border)); color: hsl(var(--muted-foreground)); font-size: 12px; }
  .statusbar .tag { margin-left: auto; font-family: "Geist Mono Variable", ui-monospace, monospace; }
  .dot.err { background: hsl(var(--destructive)); box-shadow: none; }
</style>
