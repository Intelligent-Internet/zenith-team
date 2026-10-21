# Third-party notice — core UI

The files in this folder came to Zenith Team through Intelligent Internet's own
core UI package. Most of them started as components
from two open-source projects. Their licenses still apply to those parts.

## shadcn/ui

- Files: `avatar.tsx`, `button.tsx`, `cn.ts`, `command.tsx`, `dialog.tsx`,
  `dropdown-menu.tsx`, `hover-card.tsx`, `input-group.tsx`, `input.tsx`,
  `select.tsx`, `textarea.tsx`
- Source: <https://github.com/shadcn-ui/ui>
- License: MIT, full text in [`shadcn-ui-LICENSE.txt`](shadcn-ui-LICENSE.txt)
- Copyright (c) 2023 shadcn

## AI Elements

- Files: everything in [`ai-elements/`](ai-elements/) (`attachments.tsx`,
  `conversation.tsx`, `message.tsx`, `prompt-input.tsx`)
- Source: <https://github.com/vercel/ai-elements>
- License: Apache-2.0, full text in
  [`ai-elements-LICENSE.txt`](ai-elements-LICENSE.txt)
- Copyright 2023 Vercel, Inc.

## Changes

Every file listed above has been changed from its upstream version:

- styles were rewritten to use Zenith Team's design tokens and classes;
- upstream icon imports were replaced with the app's shared icon components;
- parts the app does not use were removed, and some components were
  extended for Zenith Team.

These changes are Intelligent Internet's work and are released under the
project's MIT license.
