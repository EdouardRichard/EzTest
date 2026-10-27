import type { NextConfig } from "next"

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // The deploy/Dockerfile.web image copies the standalone build.
  output: "standalone",
}

export default nextConfig
