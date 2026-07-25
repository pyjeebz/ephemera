import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [sveltekit()],
  // The assets are committed and served from a local box; sourcemaps would just
  // bloat the embedded bundle.
  build: { sourcemap: false }
});
