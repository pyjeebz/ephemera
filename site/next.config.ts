import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /* config options here */
  cacheComponents: true,
  partialPrefetching: true,
  // `curl -fsSL https://ephemera.run/install | sh` gets the latest release's
  // install script. Temporary, so a new release is picked up at once.
  async redirects() {
    return [
      // Runs became the Sandboxes page.
      { source: "/dashboard/runs", destination: "/dashboard/sandboxes", permanent: false },
      {
        source: "/install",
        destination: "https://github.com/pyjeebz/ephemera/releases/latest/download/install.sh",
        permanent: false,
      },
      // `curl -fsSL https://ephemera.run/host | sudo sh -s -- <code>` sets up a host.
      {
        source: "/host",
        destination: "https://github.com/pyjeebz/ephemera/releases/latest/download/install-host.sh",
        permanent: false,
      },
    ]
  },
  // ephemera.run/docs is the Mintlify site, served through this one. Its
  // scripts, styles and assets load from the root, so those paths go to
  // Mintlify too.
  async rewrites() {
    const docs = "https://ephemera.mintlify.site"
    return [
      { source: "/docs", destination: `${docs}/docs` },
      { source: "/docs/:match*", destination: `${docs}/docs/:match*` },
      { source: "/_mintlify/:path*", destination: `${docs}/_mintlify/:path*` },
      { source: "/mintlify-assets/:path+", destination: `${docs}/mintlify-assets/:path+` },
      { source: "/api/request", destination: `${docs}/_mintlify/api/request` },
    ]
  },
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;
