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

<main>
  <header>
    <h1>ephemera</h1>
    <span class="tagline">boxes — fast, isolated Linux machines you use like a laptop</span>
  </header>

  {#if error}<div class="error">{error}</div>{/if}

  <section class="new">
    <input class="mono" placeholder="name a box to keep it…" bind:value={name}
           onkeydown={(e) => e.key === 'Enter' && create()} />
    <label class="toggle" class:disabled={name.trim()}>
      <input type="checkbox" bind:checked={wantDesktop} disabled={!!name.trim()} /> desktop
    </label>
    <button class="primary" onclick={create} disabled={busy}>
      {name.trim() ? 'Keep box' : 'New box'}
    </button>
  </section>

  <table>
    <thead>
      <tr><th>box</th><th>kind</th><th>state</th><th>address</th><th class="actions">actions</th></tr>
    </thead>
    <tbody>
      {#each boxes as b (b.key)}
        <tr>
          <td class="mono id">{b.id}</td>
          <td><span class="tag {b.kind}">{b.kind}</span></td>
          <td>
            <span class="dot" class:up={b.running}></span>{b.running ? 'running' : 'stopped'}
          </td>
          <td class="mono muted">{b.addr}</td>
          <td class="actions">
            {#if b.running}
              <a class="btn" href="/box/{b.machineId}" target="_blank" rel="noopener">desktop</a>
              <button onclick={() => snapshot(b)} disabled={busy}>snapshot</button>
            {/if}
            {#if b.computer && b.running}
              <button onclick={() => stop(b)} disabled={busy}>stop</button>
            {:else if b.computer}
              <button onclick={() => start(b)} disabled={busy}>start</button>
            {/if}
            <button class="danger" onclick={() => remove(b)} disabled={busy}>rm</button>
          </td>
        </tr>
      {:else}
        <tr><td colspan="5" class="empty">
          {loaded ? 'no boxes yet — name one above to keep it, or leave it blank for a throwaway' : 'loading…'}
        </td></tr>
      {/each}
    </tbody>
  </table>

  {#if snapshots.length}
    <h2>snapshots</h2>
    <table>
      <thead><tr><th>id</th><th>source</th><th>size</th><th class="actions">actions</th></tr></thead>
      <tbody>
        {#each snapshots as s (s.id)}
          <tr>
            <td class="mono id">{s.id}</td>
            <td class="mono muted">{s.source_id}</td>
            <td class="muted">{s.vcpus} vCPU · {s.mem_mib} MiB</td>
            <td class="actions"><button onclick={() => fork(s)} disabled={busy}>fork</button></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</main>

<style>
  main { max-width: 900px; margin: 0 auto; padding: 32px 20px 64px; }
  header { display: flex; align-items: baseline; gap: 14px; margin-bottom: 24px; flex-wrap: wrap; }
  h1 { margin: 0; font-size: 22px; letter-spacing: -0.01em; }
  h2 { font-size: 14px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--muted); margin: 32px 0 8px; }
  .tagline { color: var(--muted); font-size: 13px; }
  .error { background: #3a1714; border: 1px solid var(--vermilion); color: #ffd9d2; padding: 8px 12px; border-radius: 6px; margin-bottom: 16px; }

  .new { display: flex; align-items: center; gap: 10px; margin-bottom: 20px; }
  .new input.mono { flex: 1; min-width: 0; }
  .toggle { display: inline-flex; align-items: center; gap: 6px; color: var(--muted); user-select: none; white-space: nowrap; }
  .toggle.disabled { opacity: 0.4; }

  table { width: 100%; border-collapse: collapse; }
  th { text-align: left; font-weight: 500; color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: 0.05em; padding: 6px 10px; border-bottom: 1px solid var(--line); }
  td { padding: 9px 10px; border-bottom: 1px solid var(--line); vertical-align: middle; }
  .id { font-size: 13px; }
  .muted { color: var(--muted); }
  .empty { color: var(--muted); text-align: center; padding: 28px; }

  .tag { font-size: 11px; padding: 1px 7px; border-radius: 999px; border: 1px solid var(--line); }
  .tag.kept { color: var(--vermilion); border-color: var(--vermilion); }
  .tag.temp { color: var(--muted); }

  .dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: #6b625a; margin-right: 7px; vertical-align: 0px; }
  .dot.up { background: var(--green); }

  td.actions, th.actions { text-align: right; white-space: nowrap; }
  td.actions { display: flex; gap: 6px; justify-content: flex-end; }
  .btn { display: inline-block; text-decoration: none; background: var(--panel); border: 1px solid var(--line); border-radius: 6px; padding: 5px 10px; }
  .btn:hover { border-color: #4a4038; }
</style>
