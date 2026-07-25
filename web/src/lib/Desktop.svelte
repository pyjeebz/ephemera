<script>
  import { onMount } from 'svelte';
  import { connectDesktop } from '$lib/rfb.js';

  let { id } = $props();
  let canvas;
  let status = $state('connecting…');
  let statusCls = $state('');

  onMount(() => {
    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    const url = proto + location.host + '/v1/machines/' + id + '/desktop/ws';
    const session = connectDesktop(canvas, url, {
      onStatus: (t, cls) => { status = t; statusCls = cls || ''; }
    });
    return () => session.close();
  });
</script>

<div class="wrap">
  <div class="stage">
    <canvas bind:this={canvas} tabindex="0" width="800" height="600"></canvas>
  </div>
  <div class="statusbar">
    <span class="dot" class:up={statusCls === 'up'} class:err={statusCls === 'err'}></span>
    <span>{status}</span>
  </div>
</div>

<style>
  .wrap { display: flex; flex-direction: column; height: 100%; }
  .stage { flex: 1; display: grid; place-items: center; padding: 16px; min-height: 0; }
  canvas {
    background: #000; image-rendering: pixelated; max-width: 100%; max-height: 100%;
    border: 1px solid hsl(var(--border)); border-radius: var(--radius); outline: none;
    box-shadow: 0 10px 40px rgba(0,0,0,.35);
  }
  .statusbar { display: flex; align-items: center; gap: 8px; padding: 8px 16px; border-top: 1px solid hsl(var(--border)); color: hsl(var(--muted-foreground)); font-size: 12px; }
  .dot.err { background: hsl(var(--destructive)); box-shadow: none; }
</style>
