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

<header>
  <a class="back" href="/">← boxes</a>
  <span class="dot {statusCls}"></span>
  <span class="name mono">{name}</span>
  <span class="status">{status}</span>
</header>

<div class="stage">
  <canvas bind:this={canvas} tabindex="0" width="800" height="600"></canvas>
</div>

<style>
  header { display: flex; align-items: center; gap: 12px; padding: 8px 14px; background: var(--panel); border-bottom: 1px solid var(--line); }
  .back { text-decoration: none; color: var(--muted); }
  .back:hover { color: var(--cream); }
  .name { font-weight: 600; }
  .status { color: var(--muted); }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: #6b625a; }
  .dot.up { background: var(--green); }
  .dot.err { background: var(--vermilion); }
  .stage { display: grid; place-items: center; padding: 14px; height: calc(100vh - 100px); }
  canvas { background: #000; image-rendering: pixelated; max-width: 100%; max-height: 100%; box-shadow: 0 8px 40px rgba(0,0,0,.5); outline: none; }
</style>
