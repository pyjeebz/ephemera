import adapter from '@sveltejs/adapter-static';

// The web UI is a single-page app that ephemerad embeds and serves. adapter-static
// with a fallback builds a pure SPA — one index.html plus assets — which the
// daemon hands out; routing and data-loading happen on the client against the
// daemon's JSON API. The build lands in internal/webui/dist, where a go:embed
// picks it up, so `go build` produces one binary with the UI baked in.
const config = {
  kit: {
    adapter: adapter({
      pages: '../internal/webui/dist',
      assets: '../internal/webui/dist',
      fallback: 'index.html',
      precompress: false,
      strict: true
    })
  }
};

export default config;
