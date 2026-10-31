import type { NextConfig } from "next"

// Security response headers applied to every page and asset. The CSP is
// deliberately report-only: the App Router runtime and Tailwind inject inline
// styles/scripts, so an enforcing policy would break the UI before the
// violation reports have been reviewed. Flip it to Content-Security-Policy once
// the report-only baseline is clean (see docs/acceptance/p8/security-audit.md).
const securityHeaders = [
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  { key: "X-Frame-Options", value: "DENY" },
  {
    key: "Permissions-Policy",
    value: "camera=(), microphone=(), geolocation=(), payment=(), usb=(), browsing-topics=()",
  },
  {
    key: "Content-Security-Policy-Report-Only",
    value: [
      "default-src 'self'",
      "base-uri 'self'",
      "form-action 'self'",
      "frame-ancestors 'none'",
      "object-src 'none'",
      "img-src 'self' data: blob:",
      "font-src 'self' data:",
      "style-src 'self' 'unsafe-inline'",
      "script-src 'self' 'unsafe-inline' 'unsafe-eval'",
      "connect-src 'self' ws: wss: http://localhost:13333 http://127.0.0.1:13333",
    ].join("; "),
  },
]

const nextConfig: NextConfig = {
  reactStrictMode: true,
  // Dev only: the local scripts serve the UI at http://localhost:6868 and the 0.2
  // API derives its two CORS origins from EZTEST_WEB_PORT. Allowing the loopback
  // hosts here stops the Next dev server from blocking its own HMR/dev resources
  // when the UI is opened through 127.0.0.1.
  allowedDevOrigins: ["localhost", "127.0.0.1"],
  // The deploy/Dockerfile.web image copies the standalone build.
  output: "standalone",
  async headers() {
    // scripts/security-audit.ps1 asserts that this catch-all entry returns
    // securityHeaders; keep the reference explicit if this list is refactored.
    return [{ source: "/:path*", headers: securityHeaders }]
  },
}

export default nextConfig
