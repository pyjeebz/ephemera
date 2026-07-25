<script>
  import { onMount } from 'svelte';
  import { api } from '$lib/api.js';

  let boxes = $state([]);
  let snapshots = $state([]);
  let error = $state('');
  let busy = $state(false);
  let name = $state('');
  let wantDesktop = $state(false);
  let loaded = $state(false);

  async function refresh() {
    try {
      const [computers, machines, snaps] = await Promise.all([
        api.computers(), api.machines(), api.snapshots()
      ]);
      const rows = [];
      for (const c of computers) {
        rows.push({ key: 'c:' + c.name, id: c.name, kind: 'kept', running: c.running,
          addr: c.guest_ip || '—', machineId: c.machine_id || '', computer: true });
      }
      for (const m of machines) {
        if (m.computer) continue;
        rows.push({ key: 'm:' + m.id, id: m.id, kind: 'temp', running: true,
          addr: m.guest_ip || '—', machineId: m.id, computer: false });
      }
      boxes = rows;
      snapshots = snaps;
      error = '';
    } catch (e) {
      error = e.message;
    } finally {
      loaded = true;
    }
  }

  async function act(fn) {
    busy = true; error = '';
    try { await fn(); await refresh(); }
    catch (e) { error = e.message; }
    finally { busy = false; }
  }

  function create() {
    const n = name.trim();
    act(async () => {
      if (n) await api.newComputer(n);
      else await api.newMachine({ desktop: wantDesktop });
      name = ''; wantDesktop = false;
    });
  }

  const remove = (b) => act(() => (b.computer ? api.deleteComputer(b.id) : api.destroyMachine(b.id)));
  const stop = (b) => act(() => api.stopComputer(b.id));
  const start = (b) => act(() => api.startComputer(b.id));
  const snapshot = (b) => act(() => api.snapshot(b.machineId));
  const fork = (s) => act(() => api.fork(s.id));

  onMount(() => {
    refresh();
    const t = setInterval(refresh, 3000);
    return () => clearInterval(t);
  });
</script>

<svelte:head><title>ephemera</title></svelte:head>

<div class="page">
  <header class="topbar">
    <div class="brand">
      <span class="wordmark">ephemera</span>
      <span class="sep">/</span>
      <span class="crumb">boxes</span>
    </div>
    <span class="hint mono">fast, isolated Linux machines you use like a laptop</span>
  </header>

  <main>
    {#if error}<div class="alert">{error}</div>{/if}

    <div class="card compose">
      <input class="input mono" placeholder="name a box to keep it, or leave blank for a throwaway…"
             bind:value={name} onkeydown={(e) => e.key === 'Enter' && create()} />
      <label class="check" class:off={name.trim()}>
        <input type="checkbox" bind:checked={wantDesktop} disabled={!!name.trim()} />
        desktop
      </label>
      <button class="btn btn-default" onclick={create} disabled={busy}>
        {name.trim() ? 'Keep box' : 'New box'}
      </button>
    </div>

    <div class="card">
      <div class="card-head">
        <h2>Boxes</h2>
        <span class="count mono">{boxes.length}</span>
      </div>
      <table>
        <thead>
          <tr><th>Box</th><th>Kind</th><th>State</th><th>Address</th><th class="right">Actions</th></tr>
        </thead>
        <tbody>
          {#each boxes as b (b.key)}
            <tr>
              <td class="mono strong">{b.id}</td>
              <td><span class="badge badge-outline {b.kind}">{b.kind}</span></td>
              <td><span class="state"><span class="dot" class:up={b.running}></span>{b.running ? 'running' : 'stopped'}</span></td>
              <td class="mono muted">{b.addr}</td>
              <td class="right actions">
                {#if b.running}
                  <a class="btn btn-outline btn-sm" href="/box/{b.machineId}" target="_blank" rel="noopener">Desktop</a>
                  <button class="btn btn-ghost btn-sm" onclick={() => snapshot(b)} disabled={busy}>Snapshot</button>
                {/if}
                {#if b.computer && b.running}
                  <button class="btn btn-secondary btn-sm" onclick={() => stop(b)} disabled={busy}>Stop</button>
                {:else if b.computer}
                  <button class="btn btn-secondary btn-sm" onclick={() => start(b)} disabled={busy}>Start</button>
                {/if}
                <button class="btn btn-destructive btn-sm" onclick={() => remove(b)} disabled={busy}>Remove</button>
              </td>
            </tr>
          {:else}
            <tr><td colspan="5" class="empty">
              {loaded ? 'No boxes yet. Name one above to keep it, or leave it blank for a throwaway.' : 'Loading…'}
            </td></tr>
          {/each}
        </tbody>
      </table>
    </div>

    {#if snapshots.length}
      <div class="card">
        <div class="card-head"><h2>Snapshots</h2><span class="count mono">{snapshots.length}</span></div>
        <table>
          <thead><tr><th>ID</th><th>Source</th><th>Shape</th><th class="right">Actions</th></tr></thead>
          <tbody>
            {#each snapshots as s (s.id)}
              <tr>
                <td class="mono strong">{s.id}</td>
                <td class="mono muted">{s.source_id}</td>
                <td class="muted">{s.vcpus} vCPU · {s.mem_mib} MiB</td>
                <td class="right actions"><button class="btn btn-outline btn-sm" onclick={() => fork(s)} disabled={busy}>Fork</button></td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </main>
</div>

<style>
  .topbar {
    display: flex; align-items: center; justify-content: space-between; gap: 16px;
    height: 56px; padding: 0 24px;
    border-bottom: 1px solid hsl(var(--border));
    position: sticky; top: 0; background: hsl(var(--background) / .8); backdrop-filter: blur(8px); z-index: 10;
  }
  .brand { display: flex; align-items: center; gap: 10px; }
  .wordmark { font-weight: 600; font-size: 15px; letter-spacing: -0.01em; }
  .sep { color: hsl(var(--border)); }
  .crumb { color: hsl(var(--muted-foreground)); font-size: 14px; }
  .hint { color: hsl(var(--muted-foreground)); font-size: 12px; }

  main { max-width: 960px; margin: 0 auto; padding: 28px 24px 64px; display: flex; flex-direction: column; gap: 20px; }

  .alert {
    background: hsl(var(--destructive) / .1); border: 1px solid hsl(var(--destructive) / .4);
    color: hsl(var(--destructive)); padding: 10px 14px; border-radius: var(--radius); font-size: 13px;
  }

  .compose { display: flex; align-items: center; gap: 12px; padding: 12px; }
  .compose .input { flex: 1; min-width: 0; }
  .check { display: inline-flex; align-items: center; gap: 6px; color: hsl(var(--muted-foreground)); font-size: 13px; white-space: nowrap; user-select: none; }
  .check.off { opacity: .4; }

  .card-head { display: flex; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid hsl(var(--border)); }
  h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .count { color: hsl(var(--muted-foreground)); font-size: 12px; }

  table { width: 100%; border-collapse: collapse; }
  th { text-align: left; font-weight: 500; color: hsl(var(--muted-foreground)); font-size: 11px; text-transform: uppercase; letter-spacing: 0.06em; padding: 10px 16px; }
  td { padding: 11px 16px; border-top: 1px solid hsl(var(--border)); vertical-align: middle; font-size: 13px; }
  tbody tr:hover td { background: hsl(var(--muted) / .4); }
  .strong { font-weight: 500; }
  .muted { color: hsl(var(--muted-foreground)); }
  .right { text-align: right; }
  .empty { color: hsl(var(--muted-foreground)); text-align: center; padding: 40px; }

  .badge.kept { color: hsl(var(--foreground)); border-color: hsl(var(--foreground) / .3); }
  .state { display: inline-flex; align-items: center; gap: 8px; }
  .actions { display: flex; gap: 6px; justify-content: flex-end; }

  @media (max-width: 640px) {
    .hint { display: none; }
    .actions { flex-wrap: wrap; }
  }
</style>
