---
name: zenith-studio-cli
description: Create and manage Zenith Studio media inside Zenith Team, including images, video, voice, audio, music, projects, jobs, assets, and custom-skill downloads.
---

# Zenith Studio for Zenith Team

Use the daemon-managed `zenith-studio` CLI for remote operations. The Team
Agent owns reasoning, local files, composition, review and delivery under the
Run's permissions. Never start another Studio Assistant loop.

## Load the relevant route once

Use `load_skill_reference` with `skillKey: "zenith-studio-cli"` and the exact
linked paths. The key is already known; no skill search or full reference
inventory is needed. Load the common runtime and the selected route in one
tool call, with `includeReferences: false`, then reuse them within this Run:

- All Studio work: [references/zenith-team-runtime.md](references/zenith-team-runtime.md).
- Images, voice, audio and music: [references/zenith-team-media.md](references/zenith-team-media.md).
  This is the complete Team CLI lifecycle; ordinary requests need no shared
  lifecycle or project-identity guide. Load a deeper model/input guide when
  the request needs constraints not covered here.
- Any video outcome, including one clip: [references/zenith-team-video.md](references/zenith-team-video.md),
  then [resources/video-agent-mode.md](resources/video-agent-mode.md) and its
  selected creative workflow. The Team adapters override web-only execution
  requirements without removing creative, continuity or quality checks.
- Private custom-skill downloads: [references/factory-skills.md](references/factory-skills.md).
- Known projects, models, jobs, assets or account questions: use the common
  runtime and only the relevant CLI operation. A known job needs no new model
  discovery or project creation. Read command help only for an unfamiliar
  operation or a relevant error.

Keep one plan/checkpoint in the Run workspace and preserve project, node,
execution, asset and idempotency IDs. Generate only requested results; never
add paid variants or repeat successful work without authorization. Review the
actual final files, validate and commit, then call `present_output` with the
recorded source canvas as the runtime specifies.

For a named model, use the managed `models list --model` filter. For a submitted
node, use `team collect` to wait, download and export its canvas together; this
does not review or deliver the media. Preserve the review and delivery steps.
