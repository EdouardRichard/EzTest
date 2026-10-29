# Third-party notices — EzTest package `__VERSION__`

This package bundles third-party software. The authoritative license texts are
listed below; where a component ships its own license file inside the package,
that file is the binding copy.

## Runtime components

| Component | Version | License | Location in package |
|---|---|---|---|
| Node.js | `__NODE_VERSION__` | MIT (bundled `LICENSE`) | `runtime/node/` |
| Playwright | `__PLAYWRIGHT_VERSION__` | Apache-2.0 (bundled `LICENSE`) | `executor/node_modules/playwright/` |
| Chromium (Chrome for Testing build) | `__CHROMIUM_VERSION__`, Playwright revision `__CHROMIUM_REVISION__` | BSD-3-Clause plus third-party notices embedded in the binary | `playwright-browsers/chromium-__CHROMIUM_REVISION__/` |
| Chromium headless shell | `__CHROMIUM_VERSION__`, Playwright revision `__CHROMIUM_REVISION__` | BSD-3-Clause plus third-party notices embedded in the binary | `playwright-browsers/chromium_headless_shell-__CHROMIUM_REVISION__/` |
| FFmpeg | Playwright build `__FFMPEG_REVISION__` | LGPL-2.1-or-later (as distributed by the Playwright project; see the build's own notices) | `playwright-browsers/ffmpeg-__FFMPEG_REVISION__/` |
| Next.js | 16.4.3 | MIT | `web/node_modules/next/` |
| React / React DOM | 19.3.1 | MIT | `web/node_modules/react*` |
| styled-jsx | 5.1.6 | MIT | `web/node_modules/styled-jsx/` |
| @next/env | 16.4.3 | MIT | `web/node_modules/@next/env/` |
| @swc/helpers | 0.5.x | Apache-2.0 | `web/node_modules/@swc/helpers/` |
| client-only, semver, detect-libc | see `package.json` files | MIT / ISC / Apache-2.0 | `web/node_modules/` |
| pixelmatch, pngjs (executor deps) | see `executor/node_modules/*/package.json` | ISC / MIT | `executor/node_modules/` |

The Next.js standalone bundle also contains the application's own code and
its traced production dependencies; every package under `web/node_modules/`
keeps its `package.json` and any bundled `LICENSE` file.

## Go API binary

The API binary is built from the EzTest repository
(`backend/`, package `github.com/EdouardRichard/EzTest/backend`) and links the
Go standard library (BSD-3-Clause) plus the third-party Go modules below. Each
module remains under its own license (predominantly MIT, BSD-2/3-Clause and
Apache-2.0); the module license texts are available from the module sources
listed at the version shown and from the repository's `go.mod`/`go.sum`.

Module dependency list (generated at build time):

<!-- GO_MODULES -->

## EzTest

EzTest itself is licensed under the Apache License 2.0 (see `LICENSE`).

## Optional bundled PostgreSQL

By default PostgreSQL is an external prerequisite and is **not** part of this
package. If a `pgsql/` directory is present, a portable PostgreSQL 17 build is
bundled (Windows only); PostgreSQL is distributed under the PostgreSQL License
(see the license files of that distribution, typically
`pgsql/doc/COPYRIGHT` or `pgsql/share/doc`), and its bundled third-party
libraries (OpenSSL, libintl/gettext, zlib, ICU, ...) retain their own licenses
as listed in the distribution's `doc`/`share` directories.

## Notes

- The Go API binary is statically linked (`CGO_ENABLED=0`): it contains no
  system shared libraries at runtime.
- The web UI does not ship the `sharp` image optimizer or its platform
  binaries; the bundle is pure JavaScript and can run on Windows and Linux.
- Nothing in this package is signed. Verify the release `SHA256SUMS.txt`
  before running the binaries.
