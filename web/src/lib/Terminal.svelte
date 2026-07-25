<script>
  import { onMount } from 'svelte';
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';

  let { id } = $props();
  let el;
  let status = $state('connecting…');
  let statusCls = $state('');

  onMount(() => {
    const term = new Terminal({
      fontFamily: '"Geist Mono Variable", ui-monospace, SFMono-Regular, Menlo, monospace',
      fontSize: 13,
      cursorBlink: true,
      // A dark terminal on both themes — conventional, and it reads as deliberate.
      theme: {
        background: '#0a0a0b', foreground: '#e4e4e7', cursor: '#e4e4e7',
        selectionBackground: '#3f3f46', black: '#18181b', brightBlack: '#52525b'
      }
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();

    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    const ws = new WebSocket(proto + location.host + '/v1/machines/' + id + '/shell/ws');
    ws.binaryType = 'arraybuffer';
    const enc = new TextEncoder();

    function sendData(str) {
      const b = enc.encode(str);
      const msg = new Uint8Array(b.length + 1);
      msg[0] = 0; msg.set(b, 1); // 0x00 = data
      if (ws.readyState === 1) ws.send(msg);
    }
    function sendResize() {
      try { fit.fit(); } catch { /* container not laid out yet */ }
      const msg = new Uint8Array(5), dv = new DataView(msg.buffer);
      msg[0] = 1; dv.setUint16(1, term.rows); dv.setUint16(3, term.cols); // 0x01 = resize
      if (ws.readyState === 1) ws.send(msg);
    }

    const dataSub = term.onData(sendData);
    ws.onopen = () => { status = 'connected'; statusCls = 'up'; sendResize(); term.focus(); };
    ws.onmessage = (ev) => term.write(new Uint8Array(ev.data));
    ws.onclose = () => { status = 'disconnected'; statusCls = 'err'; };
    ws.onerror = () => { status = 'connection error'; statusCls = 'err'; };

    const onResize = () => sendResize();
    window.addEventListener('resize', onResize);

    return () => {
      window.removeEventListener('resize', onResize);
      dataSub.dispose();
      try { ws.close(); } catch { /* already closed */ }
      term.dispose();
    };
  });
</script>

<div class="wrap">
  <div class="stage" bind:this={el}></div>
  <div class="statusbar">
    <span class="dot" class:up={statusCls === 'up'} class:err={statusCls === 'err'}></span>
    <span>{status}</span>
  </div>
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; }
  .stage { flex: 1; min-height: 0; padding: 10px 12px; background: #0a0a0b; }
  .stage :global(.xterm) { height: 100%; }
  .statusbar { display: flex; align-items: center; gap: 8px; padding: 8px 16px; border-top: 1px solid hsl(var(--border)); color: hsl(var(--muted-foreground)); font-size: 12px; }
  .dot.err { background: hsl(var(--destructive)); box-shadow: none; }
</style>
