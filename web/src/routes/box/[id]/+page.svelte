<script>
  import { onMount } from 'svelte';
  import { page } from '$app/stores';
  import { api } from '$lib/api.js';
  import Desktop from '$lib/Desktop.svelte';
  import Video from '$lib/Video.svelte';
  import Terminal from '$lib/Terminal.svelte';

  const id = $page.params.id;
  const wantView = $page.url.searchParams.get('view'); // 'desktop' | 'terminal' | null

  let hasDesktop = $state(false);
  let ready = $state(false);
  let tab = $state('terminal'); // safe default until we learn what the box has
  let mode = $state('rfb'); // desktop pixels: 'rfb' (crisp) or 'video' (smooth)

  onMount(async () => {
    // Only a box booted with the desktop image has a graphical desktop; without
    // this, opening the desktop on a plain box just hangs on a connection to
    // nothing. So ask the daemon, and offer the desktop only when it exists.
    try {
      const m = await api.machine(id);
      hasDesktop = !!m.desktop;
    } catch { /* fall back to terminal-only */ }
    tab = wantView === 'terminal' ? 'terminal' : hasDesktop ? 'desktop' : 'terminal';
    ready = true;
  });
</script>

<svelte:head><title>{id} — ephemera</title></svelte:head>

<div class="box">
  <header class="topbar">
    <div class="brand">
      <a class="back" href="/">← boxes</a>
      <span class="sep">/</span>
      <span class="name mono">{id}</span>
    </div>

    <div class="controls">
      {#if ready && tab === 'desktop'}
        <div class="seg" role="group" aria-label="desktop mode">
          <button class="seg-btn" class:on={mode === 'rfb'} onclick={() => (mode = 'rfb')} title="Raw framebuffer — crisp and low-latency">Crisp</button>
          <button class="seg-btn" class:on={mode === 'video'} onclick={() => (mode = 'video')} title="H.264 video — smoother motion, a little latency">Smooth</button>
        </div>
      {/if}
      <div class="tabs" role="tablist">
        {#if hasDesktop}
          <button class="tab" class:active={tab === 'desktop'} onclick={() => (tab = 'desktop')}>Desktop</button>
        {/if}
        <button class="tab" class:active={tab === 'terminal'} onclick={() => (tab = 'terminal')}>Terminal</button>
      </div>
    </div>
  </header>

  <div class="view">
    {#if ready}
      {#if tab === 'desktop' && hasDesktop}
        {#if mode === 'rfb'}
          {#key 'rfb'}<Desktop {id} />{/key}
        {:else}
          {#key 'video'}<Video {id} />{/key}
        {/if}
      {:else}
        <Terminal {id} />
      {/if}
    {/if}
  </div>
</div>

<style>
  .box { display: flex; flex-direction: column; height: 100vh; }
  .topbar {
    display: flex; align-items: center; justify-content: space-between; gap: 16px;
    height: 52px; padding: 0 20px; border-bottom: 1px solid hsl(var(--border)); flex: none;
  }
  .brand { display: flex; align-items: center; gap: 10px; }
  .back { color: hsl(var(--muted-foreground)); font-size: 13px; }
  .back:hover { color: hsl(var(--foreground)); }
  .sep { color: hsl(var(--border)); }
  .name { font-weight: 600; font-size: 14px; }

  .controls { display: flex; align-items: center; gap: 14px; }

  .seg { display: inline-flex; border: 1px solid hsl(var(--border)); border-radius: calc(var(--radius) - 2px); overflow: hidden; }
  .seg-btn {
    background: transparent; border: none; color: hsl(var(--muted-foreground));
    font: inherit; font-size: 12px; padding: 5px 11px; cursor: pointer;
  }
  .seg-btn:hover { color: hsl(var(--foreground)); }
  .seg-btn.on { background: hsl(var(--secondary)); color: hsl(var(--foreground)); font-weight: 500; }

  .tabs { display: flex; gap: 2px; }
  .tab {
    background: transparent; border: none; color: hsl(var(--muted-foreground));
    font: inherit; font-size: 13px; padding: 6px 12px; cursor: pointer; border-radius: calc(var(--radius) - 2px);
  }
  .tab:hover { color: hsl(var(--foreground)); background: hsl(var(--accent)); }
  .tab.active { color: hsl(var(--foreground)); background: hsl(var(--secondary)); font-weight: 500; }

  .view { flex: 1; min-height: 0; }
</style>
