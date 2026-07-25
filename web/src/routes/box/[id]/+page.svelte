<script>
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { connectDesktop } from '$lib/rfb.js';

  const id = $page.params.id;
  let canvas;
  let status = $state('connecting…');
  let statusCls = $state('');
  let name = $state('desktop');

  onMount(() => {
    const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
    const url = proto + location.host + '/v1/machines/' + id + '/desktop/ws';
    const session = connectDesktop(canvas, url, {
      onStatus: (t, cls) => { status = t; statusCls = cls || ''; },
      onName: (n) => { name = n || 'desktop'; }
    });
    return () => session.close();
  });
</script>

<svelte:head><title>{name} — ephemera</title></svelte:head>

<header class="topbar">
  <div class="brand">
    <a class="back" href="/">← boxes</a>
    <span class="sep">/</span>
    <span class="name mono">{name}</span>
  </div>
  <span class="state"><span class="dot" class:up={statusCls === 'up'} class:err={statusCls === 'err'}></span>{status}</span>
</header>

<div class="stage">
  <canvas bind:this={canvas} tabindex="0" width="800" height="600"></canvas>
</div>

<style>
  .topbar {
    display: flex; align-items: center; justify-content: space-between; gap: 16px;
    height: 52px; padding: 0 20px; border-bottom: 1px solid hsl(var(--border));
  }
  .brand { display: flex; align-items: center; gap: 10px; }
  .back { color: hsl(var(--muted-foreground)); font-size: 13px; }
  .back:hover { color: hsl(var(--foreground)); }
  .sep { color: hsl(var(--border)); }
  .name { font-weight: 600; font-size: 14px; }
  .state { display: inline-flex; align-items: center; gap: 8px; color: hsl(var(--muted-foreground)); font-size: 13px; }
  .dot.err { background: hsl(var(--destructive)); box-shadow: none; }

  .stage { display: grid; place-items: center; padding: 16px; height: calc(100vh - 52px); }
  canvas {
    background: #000; image-rendering: pixelated; max-width: 100%; max-height: 100%;
    border: 1px solid hsl(var(--border)); border-radius: var(--radius); outline: none;
    box-shadow: 0 10px 40px rgba(0,0,0,.35);
  }
</style>
