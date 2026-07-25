<script>
  import { page } from '$app/stores';
  import Desktop from '$lib/Desktop.svelte';
  import Terminal from '$lib/Terminal.svelte';

  const id = $page.params.id;
  let tab = $state('desktop');
</script>

<svelte:head><title>{id} — ephemera</title></svelte:head>

<div class="box">
  <header class="topbar">
    <div class="brand">
      <a class="back" href="/">← boxes</a>
      <span class="sep">/</span>
      <span class="name mono">{id}</span>
    </div>
    <div class="tabs" role="tablist">
      <button class="tab" class:active={tab === 'desktop'} onclick={() => (tab = 'desktop')}>Desktop</button>
      <button class="tab" class:active={tab === 'terminal'} onclick={() => (tab = 'terminal')}>Terminal</button>
    </div>
  </header>

  <div class="view">
    {#if tab === 'desktop'}
      <Desktop {id} />
    {:else}
      <Terminal {id} />
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

  .tabs { display: flex; gap: 2px; }
  .tab {
    background: transparent; border: none; color: hsl(var(--muted-foreground));
    font: inherit; font-size: 13px; padding: 6px 12px; cursor: pointer; border-radius: calc(var(--radius) - 2px);
  }
  .tab:hover { color: hsl(var(--foreground)); background: hsl(var(--accent)); }
  .tab.active { color: hsl(var(--foreground)); background: hsl(var(--secondary)); font-weight: 500; }

  .view { flex: 1; min-height: 0; }
</style>
