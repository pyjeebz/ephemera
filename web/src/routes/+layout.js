// A single-page app served by ephemerad: no server-side rendering, no
// prerendering. The daemon hands out one index.html and Svelte routes on the
// client; all data comes from the daemon's JSON API at runtime.
export const ssr = false;
export const prerender = false;
