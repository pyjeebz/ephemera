// The daemon's JSON API, same origin. ephemerad serves this SPA and the API from
// one listener, so relative paths just work — no base URL, no CORS.

async function req(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const j = await res.json();
      if (j && j.error) msg = j.error;
    } catch { /* non-JSON error body; keep the status text */ }
    throw new Error(msg);
  }
  if (res.status === 204) return null;
  return res.json();
}

export const api = {
  machines: () => req('GET', '/v1/machines').then((r) => r.machines ?? []),
  machine: (id) => req('GET', '/v1/machines/' + id),
  computers: () => req('GET', '/v1/computers').then((r) => r.computers ?? []),
  snapshots: () => req('GET', '/v1/snapshots').then((r) => r.snapshots ?? []),

  newMachine: (opts) => req('POST', '/v1/machines', opts),
  newComputer: (name) => req('POST', '/v1/computers', { name }),

  destroyMachine: (id) => req('DELETE', '/v1/machines/' + id),
  startComputer: (name) => req('POST', '/v1/computers/' + encodeURIComponent(name) + '/start'),
  stopComputer: (name) => req('POST', '/v1/computers/' + encodeURIComponent(name) + '/stop'),
  deleteComputer: (name) => req('DELETE', '/v1/computers/' + encodeURIComponent(name)),

  snapshot: (id) => req('POST', '/v1/machines/' + id + '/snapshot'),
  fork: (snapId) => req('POST', '/v1/snapshots/' + snapId + '/fork')
};
