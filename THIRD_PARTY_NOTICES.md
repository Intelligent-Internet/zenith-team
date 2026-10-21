# Third-party notices

Zenith Team is released under the MIT License, copyright Intelligent Internet.
The repository also contains some material made by other people. That
material keeps its own license, listed below. Each item's full license text
sits next to it in the repository.

## Code and assets stored in this repository

### shadcn/ui components

- Path: `frontend/src/shared/ui/core/` (the `.tsx` files at the top level and `cn.ts`)
- Source: <https://github.com/shadcn-ui/ui>
- License: MIT
- Copyright (c) 2023 shadcn
- License text and list of changes: `frontend/src/shared/ui/core/shadcn-ui-LICENSE.txt`,
  `frontend/src/shared/ui/core/NOTICE.md`

### AI Elements components

- Path: `frontend/src/shared/ui/core/ai-elements/`
- Source: <https://github.com/vercel/ai-elements>
- License: Apache-2.0
- Copyright 2023 Vercel, Inc.
- These files were changed for Zenith Team. License text and list of changes:
  `frontend/src/shared/ui/core/ai-elements-LICENSE.txt`,
  `frontend/src/shared/ui/core/NOTICE.md`

### Iconsax icons

- Path: `frontend/src/assets/iconsax/`
- Source: <https://iconsax.io>, free icons only (no Pro icons), taken from the
  `iconsax@0.1.1` npm package
- License: [Iconsax Free License for Developers](https://docs.iconsax.io/license-and-terms/license).
  It allows sharing source code that includes the free icons, as long as
  this notice is kept. It is not an open-source license.
- Copyright: Iconsax
- These icons are **not** covered by this project's MIT license. To use them
  in another project, get them from iconsax.io under their license. Details
  and the list of local variants: `frontend/src/assets/iconsax/NOTICE.md`

### Web app fonts

Self-hosted WOFF2 files from Fontsource 5.3.0, all under the SIL Open Font
License 1.1. Path: `frontend/src/app/fonts/` (license text per font in the
same folder, sources in `NOTICE.md`).

| Font | Copyright |
|---|---|
| Lato | Copyright (c) 2010-2011 by tyPoland Lukasz Dziedzic, with Reserved Font Name "Lato" |
| Atkinson Hyperlegible Next | Copyright 2020-2024 The Atkinson Hyperlegible Next Project Authors |
| Noto Sans | Copyright 2022 The Noto Project Authors |
| Roboto Mono | Copyright 2015 The Roboto Mono Project Authors |
| Geist Mono, Geist Pixel | Copyright 2024 The Geist Project Authors |
| OpenDyslexic | Copyright (c) 2019 Abbie Gonzalez, with Reserved Font Name OpenDyslexic |

### Whiteboard export font

- Path: `server/internal/canvas/fonts/NotoSans-Regular.ttf`
- Source: <https://github.com/notofonts/noto-fonts>
- License: SIL Open Font License 1.1, text in `server/internal/canvas/fonts/OFL.txt`
- Copyright 2018 The Noto Project Authors

### Unicode emoji data

- Path: `server/internal/conversation/emoji/unicode-16.tsv`
- Source: Unicode Emoji 16.0 `emoji-test.txt`,
  <https://unicode.org/Public/emoji/16.0/emoji-test.txt>
- License: Unicode License v3, text in `server/internal/conversation/emoji/LICENSE.txt`
- Copyright © 1991-2026 Unicode, Inc.

### Tux, the Linux penguin

- Path: `frontend/src/assets/brands/linux.svg`
- Tux was drawn by Larry Ewing (lewing@isc.tamu.edu) with The GIMP. He allows
  use and changes as long as he and The GIMP are credited.

## Intelligent Internet content kept as pinned files

These are Intelligent Internet's own work. They are part of this project and
fall under its MIT license.

- **Workflow catalog**, `server/internal/platformbundle/zenith-workflows.bundle.tar`:
  145 Workflows as 290 YAML and Markdown files, pinned from a separate
  Intelligent Internet repository by `zenith-workflows.lock.json`. The tar
  holds no license file of its own.
- **Zenith Studio pin**, `server/internal/daemon/tool_releases/zenith-studio.lock.json`:
  version and checksum metadata only. No Studio code is stored here.

## Dependencies

Libraries installed by a package manager are **not** stored in this
repository. They are downloaded when you build or run Zenith Team, and each
one stays under its own license. Counts below were taken on 2026-10-12.

**Go server** (`server/go.mod`, 148 modules): Apache-2.0 63, MIT 49,
BSD-3-Clause 23, BSD-2-Clause 4, ISC 1, and 8 modules that combine these
licenses (Apache-2.0 with MIT and/or BSD-3-Clause). None are copyleft.

**Web and desktop app** (`frontend/`, pnpm): 694 runtime packages
(1,096 with build and test tools). Runtime packages: MIT 575, ISC 53,
Apache-2.0 25, BSD-3-Clause 13, BSD-2-Clause 6, BlueOak-1.0.0 5, MPL-2.0 3,
Unlicense 2, 0BSD 1, CC-BY-4.0 1, FSL-1.1-Apache-2.0 1, no license stated 1,
and 8 with combined or dual licenses. Packages worth knowing about:

- MPL-2.0: `lightningcss`, `lightningcss-darwin-arm64`, `mtx-decompressor`.
- Dual-licensed; Zenith Team uses them under the permissive choice:
  `jszip` (MIT or GPL-3.0), `machina` (MIT or GPL), `unorm` (MIT or GPL-2.0,
  build tools only), `dompurify` (MPL-2.0 or Apache-2.0),
  `json-schema` (AFL-2.1 or BSD-3-Clause).
- Data under Creative Commons: `caniuse-lite` (CC-BY-4.0); build tools only:
  `spdx-exceptions` (CC-BY-3.0), `spdx-license-ids` (CC0-1.0).
- Functional Source License (source-available, not open source): `sentry`
  (FSL-1.1-Apache-2.0), pulled in by the Sentry SDK; `@sentry/cli` and
  `@sentry/cli-darwin` (FSL-1.1-MIT), build tools only.
- No license stated: `buffers@0.1.1`, pulled in by `exceljs`.

**Voice agent** (`voice-agent/uv.lock`, 72 packages): Apache-2.0 26,
MIT 23, MIT-0 1, MIT-CMU 1, BSD-3-Clause 9, BSD-2-Clause 2, PSF-2.0 2, ISC 1,
MPL-2.0 1 (`certifi`), and 6 with combined licenses, among them `tqdm`
(MPL-2.0 and MIT) and `livekit-local-inference` (Apache-2.0 plus the LiveKit
model license for its bundled models).

**Downloaded at build or run time:** the Zenith Studio CLI
(`@zenith-studio/cli`, MIT, Intelligent Internet) and its `hyperframes`
dependency (Apache-2.0, HeyGen). Desktop builds include Electron (MIT), which
ships the Chromium and other notices inside the app as `LICENSE` and
`LICENSES.chromium.html`.

## Trademarks

Third-party names and logos belong to their owners. Zenith Team shows them only
to identify the products and services it works with. Showing them does not mean
those owners endorse Zenith Team.

Names and logos that appear in this repository include: Anthropic and Claude;
OpenAI and Codex; GitHub; Google, Gmail, Google Calendar, Google Drive and
Google Maps; Meta, Facebook, Instagram and React; LinkedIn; X; TikTok; Slack;
Notion; Figma; Stripe; Apple and macOS; and Linux (a registered
trademark of Linus Torvalds) with its Tux mascot.
