<script>
  // Self-hosted Geist (Vercel's typeface); Vite bundles the woff2 into the build,
  // so it ships embedded with no CDN.
  import '@fontsource-variable/geist';
  import '@fontsource-variable/geist-mono';
  import '../app.css';
  let { children } = $props();
</script>

{@render children()}
