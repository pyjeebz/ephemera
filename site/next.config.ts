import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /* config options here */
  cacheComponents: true,
  partialPrefetching: true,
  // `curl -fsSL https://ephemera.run/install | sh` gets the latest release's
  // install script. Temporary, so a new release is picked up at once.
  async redirects() {
    return [
      {
        source: "/install",
        destination: "https://github.com/pyjeebz/ephemera/releases/latest/download/install.sh",
        permanent: false,
      },
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
