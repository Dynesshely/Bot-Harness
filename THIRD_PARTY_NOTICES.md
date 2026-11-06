# Third-Party Notices

Bot Harness is licensed under the **Apache License 2.0** (see [LICENSE](LICENSE)).
It depends on the third-party components listed below. This file is provided for
attribution and license-compliance purposes; it does not modify any upstream
license.

The binary distributed from this repository is built from `cmd/bot-harness` and
embeds the admin SPA from `web/dist` plus four font files. Components are grouped
by whether they are actually linked into / embedded in that binary.

---

## 1. Go modules linked into the binary

The following modules are reachable from `go list -deps ./cmd/bot-harness` and
are therefore part of the distributed binary. All are permissively licensed.

| Module | Version | License | Copyright notice |
|---|---|---|---|
| github.com/dustin/go-humanize | v1.0.1 | MIT | Copyright (c) 2005-2008 Dustin Sallings <dustin@spy.net> |
| github.com/google/uuid | v1.6.0 | BSD-3-Clause | Copyright (c) 2009,2014 Google Inc. All rights reserved. |
| github.com/gorilla/websocket | v1.5.3 | BSD-3-Clause | Copyright (c) 2013 The Gorilla WebSocket Authors. All rights reserved. |
| github.com/jackc/pgpassfile | v1.0.0 | MIT | Copyright (c) 2019 Jack Christensen |
| github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | MIT | Copyright (c) 2020 Jack Christensen |
| github.com/jackc/pgx/v5 | v5.12.0 | MIT | Copyright (c) 2013-2021 Jack Christensen |
| github.com/jackc/puddle/v2 | v2.2.3 | MIT | Copyright (c) 2018 Jack Christensen |
| github.com/joho/godotenv | v1.5.1 | MIT | Copyright (c) 2013 John Barton |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | Copyright (c) 2012 The Go Authors. All rights reserved. |
| golang.org/x/image | v0.47.0 | BSD-3-Clause | Copyright 2009 The Go Authors. All rights reserved. |
| golang.org/x/sync | v0.24.0 | BSD-3-Clause | Copyright 2009 The Go Authors. All rights reserved. |
| golang.org/x/sys | v0.49.0 | BSD-3-Clause | Copyright 2009 The Go Authors. All rights reserved. |
| golang.org/x/text | v0.43.0 | BSD-3-Clause | Copyright 2009 The Go Authors. All rights reserved. |
| modernc.org/libc | v1.77.1 | BSD-3-Clause | Copyright (c) 2017 The Libc Authors. All rights reserved. |
| modernc.org/mathutil | v1.7.1 | BSD-3-Clause | Copyright (c) 2014 The mathutil Authors. All rights reserved. |
| modernc.org/memory | v1.12.1 | BSD-3-Clause | Copyright (c) 2017 The Memory Authors. All rights reserved. |
| modernc.org/sqlite | v1.60.1 | BSD-3-Clause | Copyright (c) 2017 The Sqlite Authors. All rights reserved. |

The Go standard library and runtime (BSD-3-Clause, Copyright 2009 The Go Authors)
are also statically linked into the binary.

The full license text of each module is available in the Go module cache after
`go mod download`, or from the upstream repository at the version listed above.

## 2. Go modules in the module graph but not linked

The following modules appear in `go list -m all` (they are test-only,
toolchain-only, or transitive requirements of tooling) but **no package from
them is linked into the distributed binary**. They are listed for transparency.

| Module | Version | License | Copyright notice |
|---|---|---|---|
| github.com/davecgh/go-spew | v1.1.1 | ISC | Copyright (c) 2012-2016 Dave Collins <dave@davec.name> |
| github.com/google/pprof | v0.0.0-20260802141513-ef3492d7dac3 | Apache-2.0 | Copyright 2014 Google Inc. |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | **MPL-2.0** | Copyright (c) 2014 HashiCorp, Inc. |
| github.com/kr/pretty | v0.3.0 | MIT | Copyright (c) 2012 Keith Rarick |
| github.com/mattn/go-isatty | v0.0.24 | MIT | Copyright (c) Yasuhiro MATSUMOTO <mattn.jp@gmail.com> |
| github.com/ncruces/go-strftime | v1.0.0 | MIT | Copyright (c) 2022 Nuno Cruces |
| github.com/pmezard/go-difflib | v1.0.0 | BSD-3-Clause | Copyright (c) 2013, Patrick Mezard |
| github.com/stretchr/objx | v0.1.0 | MIT | Copyright (c) 2014 Stretchr, Inc. |
| github.com/stretchr/testify | v1.11.1 | MIT | Copyright (c) 2012-2020 Mat Ryer, Tyler Bunnell and contributors. |
| gopkg.in/check.v1 | v1.0.0-20201130134442-10cb98267c6c | BSD-2-Clause | Copyright (c) 2010-2013 Gustavo Niemeyer |
| gopkg.in/yaml.v3 | v3.0.1 | Apache-2.0 (and MIT for the Go port) | Copyright (c) 2006-2011 Kirill Simonov; Copyright 2011-2019 Canonical Ltd. |
| golang.org/x/mod | v0.41.0 | BSD-3-Clause | Copyright 2009 The Go Authors. All rights reserved. |
| golang.org/x/tools | v0.51.0 | BSD-3-Clause | Copyright 2009 The Go Authors. All rights reserved. |
| modernc.org/cc/v4 | v4.29.7 | BSD-3-Clause | Copyright (c) 2014 The CC Authors. All rights reserved. |
| modernc.org/ccgo/v4 | v4.36.1 | BSD-3-Clause | Copyright (c) 2017 The CCGO Authors. All rights reserved. |
| modernc.org/fileutil | v1.4.0 | BSD-3-Clause | Copyright (c) 2014 The fileutil Authors. All rights reserved. |
| modernc.org/gc/v2 | v2.6.5 | BSD-3-Clause | Copyright (c) 2016 The GC Authors. All rights reserved. |
| modernc.org/gc/v3 | v3.1.5 | BSD-3-Clause | Copyright (c) 2016 The GC Authors. All rights reserved. |
| modernc.org/goabi0 | v0.2.0 | BSD-3-Clause | Copyright (c) 2025 The goabi0 Authors. All rights reserved. |
| modernc.org/opt | v0.2.0 | BSD-3-Clause | Copyright (c) 2019 The Opt Authors. All rights reserved. |
| modernc.org/sortutil | v1.2.1 | BSD-3-Clause | Copyright (c) 2014 The sortutil Authors. All rights reserved. |
| modernc.org/strutil | v1.2.1 | BSD-3-Clause | Copyright (c) 2014 The strutil Authors. All rights reserved. |
| modernc.org/token | v1.1.0 | BSD-3-Clause | Copyright (c) 2009 The Go Authors. All rights reserved. |

> **MPL-2.0 note (hashicorp/golang-lru/v2).** This module is required by
> `go.mod`'s module graph but is not linked into the binary. No MPL-2.0 code is
> distributed in the release artifacts. If a future change starts importing it,
> the MPL-2.0 source must remain available and the file-level copyleft
> obligations apply to the MPL-covered files.

## 3. Embedded fonts — SIL Open Font License 1.1

Four OFL-1.1 fonts are embedded into the binary with `go:embed` and used by the
table-to-PNG renderer (`internal/render`). The complete OFL 1.1 texts are kept
next to the font files:

| Font | File | License text | Copyright |
|---|---|---|---|
| Noto Sans CJK SC Regular | `internal/render/fonts/NotoSansCJKsc-Regular.otf` | `internal/render/fonts/OFL.txt` | Copyright 2014-2021 Adobe (http://www.adobe.com/). Noto is a trademark of Google Inc. |
| Noto Emoji (variable) | `internal/render/fonts/NotoEmoji-Variable.ttf` | `internal/render/fonts/NotoEmoji-OFL.txt` | Copyright 2013 Google LLC |
| Noto Sans Math Regular | `internal/render/fonts/NotoSansMath-Regular.ttf` | `internal/render/fonts/NotoSansMath-OFL.txt` | Copyright 2022 The Noto Project Authors (https://github.com/notofonts/math) |
| Noto Sans Symbols2 Regular | `internal/render/fonts/NotoSansSymbols2-Regular.ttf` | `internal/render/fonts/NotoSansSymbols2-OFL.txt` | Copyright 2022 The Noto Project Authors (https://github.com/notofonts/symbols) |

These fonts are distributed under the SIL Open Font License, Version 1.1, and
are **not** covered by the Apache License 2.0. They are embedded unmodified.
The OFL permits embedding and redistribution as part of a larger work, provided
the copyright notice and license are retained (they are, in this file and in
`internal/render/fonts/`) and the fonts are not sold by themselves. The Reserved
Font Names (if any) are not used for modified versions: Bot Harness embeds the
unmodified font files.

## 4. Web admin UI — runtime dependencies bundled into `web/dist`

The admin SPA is built with Vite and bundled into the Go binary. Only the
following two packages are shipped in the runtime bundle; both are MIT-licensed
and their license texts are retained in the bundle / repository.

| Package | Version | License | Copyright |
|---|---|---|---|
| vue | 3.5.43 | MIT | Copyright (c) 2018-present, Yuxi (Evan) You |
| vue-router | 5.3.1 | MIT | Copyright (c) 2019-present Eduardo San Martin Morote |

## 5. Build-time npm tooling — not shipped in the distributed artifacts

The following tools are used to build `web/dist` but **no code from them is
included in the Go binary or the released container image** (the image copies
only the already-built `web/dist`). They are listed because their licenses
appear in the build toolchain.

| Package | Version | License | Copyright |
|---|---|---|---|
| typescript | 5.9.3 | Apache-2.0 | Copyright (c) Microsoft Corporation |
| lightningcss | 1.32.0 / 1.33.0 | **MPL-2.0** | Copyright (c) 2022 Devon Govett |
| vite | 8.3.1 | MIT | Copyright (c) 2019-present, Yuxi (Evan) You and Vite contributors |
| @vitejs/plugin-vue | 6.0.9 | MIT | Copyright (c) 2021-present, Yuxi (Evan) You |
| tailwindcss / @tailwindcss/vite | 4.3.3 | MIT | Copyright (c) Tailwind Labs, Inc. |
| vue-tsc | 3.3.11 | MIT | Copyright (c) 2021-present Johnson Chu |
| vitest | 5.0.5 | MIT | Copyright (c) 2021-present Anthony Fu and Vitest contributors |
| jsdom | 30.1.3 | MIT | Copyright (c) 2010 Elijah Insua, 2011-2024 jsdom contributors |

> **MPL-2.0 note (lightningcss).** lightningcss is a build-time dependency only
> and is not distributed in the binary or the released image. Its MPL-2.0 source
> is available from the npm registry / upstream repository.

## 6. Container base images (separate works)

Released container images are built from the following base images. These are
**separate works**, not linked into the Bot Harness source; each image carries
its own license files, which are available from the upstream image and remain in
force in the published image.

| Image | Role | License (summary) |
|---|---|---|
| `alpine:3.21` | Runtime base image | Alpine Linux and its packages use various licenses (e.g. musl MIT, BusyBox GPL-2.0). See `/etc/license` / package metadata in the image. |
| `node:24-alpine` | Build stage only (not in the final image) | Node.js MIT plus Alpine package licenses. |
| `golang:1.27-alpine` | Build stage only (not in the final image) | Go BSD-3-Clause plus Alpine package licenses. |
| `postgres:17-alpine` | PostgreSQL service (docker compose) | PostgreSQL License (permissive, BSD-like). |
| `searxng/searxng:2026.11.1-9ded3bec1` | Search sidecar (docker compose) | **AGPL-3.0**. Distributed as a separate container image; it is not linked into or combined with Bot Harness code. Review the AGPL obligations before exposing a modified SearXNG instance. |

## 7. Reporting / questions

If you believe a component is missing from this file or a notice is inaccurate,
please open an issue using the repository issue templates. Security-sensitive
reports should follow [SECURITY.md](SECURITY.md), not a public issue.
