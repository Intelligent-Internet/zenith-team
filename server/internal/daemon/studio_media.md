# Zenith Team images, voice, audio and music

Use this route with `references/zenith-team-runtime.md`. It covers the CLI
lifecycle for non-video media without loading unrelated video guides. Video
work must first follow the video workflow; this lifecycle can then execute
its planned media nodes without replacing planning, assembly or review.

## Decide and prepare

1. Map the requested output to a command and catalog kind:

   | Output | Generation command | Catalog kind | Content flag |
   | --- | --- | --- | --- |
   | Image | `generate image` | `image` | `--prompt` |
   | Spoken voice | `generate voice` | `voice` | `--text` |
   | Sound/audio | `generate audio` | `audio` | `--prompt` |
   | Music | `generate music` | `audio` | `--prompt` |

   Music is an audio alias. A known submitted job skips discovery and
   generation: resume its checkpoint after checking this Run's Project binding.
2. Reuse the selected model's verified capabilities from this request/continuation
   if available. Otherwise use the managed filter:
   `zenith-studio --json models list --kind KIND --model "EXACT_ID_OR_DISPLAY_NAME"`.
   This returns only matching models with their full capabilities, without
   requiring a separate filtering script or printing the entire catalog first.
   If no model was named, query `models list --kind KIND` and return
   compatible choices with their IDs, names and relevant capabilities, not
   every model. Use an available model and supported parameters; ask for a
   choice only if the user's preferences leave multiple compatible options.
   Do not invent models, input roles or parameters.
3. Resolve-or-create the Conversation's Project using
   `zenith-studio --json projects create --title "Creative subject"` in the
   same tool batch. This returns the existing binding if one exists; the
   launcher creates only when unbound. No preceding project list is needed
   for an authorized generation request. Never adopt a Project from another Conversation.
   Resolve paths through the supplied executable. Its per-operation checks
   replace separate setup/auth/version/resolve calls on the normal path.
4. Decide the prompt/text, references, model, parameters and a meaningful
   one-to-three-word node label. Choose a unique checkpoint path; the CLI
   records the exact request and stable idempotency key **before** submission.
   Keep the selected model's capabilities with the decision when a human model
   choice requires a continuation. Let the CLI own its receipt; never overwrite
   another request's trace in the shared workspace. A simple result needs no
   separate planning and review documents.

Preserve reference identity/style and every dependent media lock. Reuse a
usable Studio node/asset instead of uploading it again. Upload a local image
reference only when it has no reusable Studio identity, through `assets upload`.
For image generation use catalog-supported `reference_image` inputs via
`--reference-node reference_image=NODE_ID` or
`--reference-asset reference_image=ASSET_ID`; retain their order. Load the
relevant model/input guide for other reference roles or an unsupported operation.

For voice, preserve the approved literal script, pronunciation, language and
voice choice. For sound/music, preserve requested duration, lyrics or intentional
instrumental mode, and other supported controls. Do not silently drop text or
lyrics to fit a limit. Split voice/audio inputs over 4,000 characters at clean
semantic boundaries, retaining every part and its order. If the user needs one
finished file, assemble and review the complete result before delivery. Keep
the actual source nodes in its canvas; do not map a source node to locally
composed bytes. Any video-related result stays on the video route.

## Execute the decided work together

The pinned CLI **0.1.15-beta.0** owns the generation checkpoint. Create its
parent directory, then run the supplied executable twice in one execution call:
`zenith-studio --json generate KIND ... --checkpoint output/UNIQUE-intent.json &&
zenith-studio --json jobs collect --checkpoint output/UNIQUE-intent.json
--output-dir output`. The generation command retains the
chosen model, label, prompt/text, parameters and native references. It saves the
exact request and stable key before submission and the real IDs afterwards.
No Python or JSON-extraction script is needed. Repeating the identical command
with its checkpoint reuses the recorded node, or the same key for an uncertain
submission; a changed request, account or API is refused. Review and delivery
below remain required. Never replace a checkpoint/key to recover a paid request.

Keep submit → checkpoint → `jobs collect` in one execution call. Do not split
mechanical steps into separate Agent turns. Run commands with the global
`--json` flag, require exit success and
`ok: true` after every step, and stop on the original error. Do not return to
the Agent between mechanical steps when the previous result supplies the next
arguments. Yield the script process while it waits; resume that same process.

- Submit `generate KIND --project PROJECT_ID --model MODEL_ID --label "Media
  subject" --prompt "REVIEWED PROMPT" --checkpoint output/UNIQUE-intent.json`, using
  `--text "APPROVED SCRIPT"` for voice and only supported `--param NAME=VALUE`.
  The CLI persists the project/node/execution/idempotency IDs before waiting.
  An uncertain submit outcome can retry only the identical request with the
  same key. Never use a new key to resolve a timeout.
- On the normal single-node path, run
  `zenith-studio --json jobs collect --checkpoint output/UNIQUE-intent.json --output-dir output`
  immediately after saving the submit IDs. It executes that wait, download
  and export sequence below without another Agent turn between them. Use its
  returned unique paths; do not choose a generic filename such as `image.png`.
  The explicit commands below are for resuming a failed step or assembling
  several outputs whose final canvas ordinals must be decided together.
- For a manual wait, use `jobs wait PROJECT_ID NODE_ID --timeout 1800 --poll-interval 2`
  and let the CLI poll. Do not also wait separately before `team collect` or
  interleave repeated status calls. A timeout does not cancel remote work;
  retain IDs and resume that job. Never regenerate a successful result without
  explicit regeneration authorization.
- Download `assets download PROJECT_ID NODE_ID --output WORKSPACE_PATH`.
  Choose the extension from the job's `mime_type` (`image/png` → `.png`,
  `image/jpeg` → `.jpg`, `image/webp` → `.webp`, `audio/mpeg` → `.mp3`,
  `audio/wav` → `.wav`, `video/mp4` → `.mp4`). Preserve the returned bytes.
  Record asset identity and the local path. On continuation inspect and reuse
  matching downloaded files rather than overwriting or regenerating them.
- Export `canvas export PROJECT_ID --node NODE_ID --output-item NODE_ID:0
  --output output/source-NODE_ID.canvas.json`. Repeat source nodes and set
  item ordinals to match the actual ordered delivery. Map only a node's
  downloaded media; retain source nodes without an output mapping for local
  transformations. Follow the runtime's existing-canvas reuse rules. Never
  upload already generated media back to Studio.

For several requested independent outputs, batch independent work with bounded
concurrency and a separate checkpoint/key per node. Do not parallelize dependent
segments or create unrequested variants. Reuse completed nodes after a partial
failure. Stop and diagnose authentication, credit, input and provider failures
instead of blindly submitting more paid work.

## Review and deliver

Inspect the actual image for requested content and reference fidelity. For
voice, audio and music, check the actual sound against the script/lyrics,
language, voice, requested duration, ending and continuity as applicable;
metadata alone is not a content review. Probe format, dimensions/duration and
audio tracks where relevant. If a required inspection tool is unavailable,
report that limitation instead of claiming the check passed.
Read the probe's actual output before recording dimensions or duration in the
checkpoint; the requested resolution is not evidence of the file's dimensions.

After actual content review, batch the metadata probe, checkpoint update,
required workspace validation, `git status`, staging only this request's files,
and commit in one execution call with failure checks. Stop on a validation or
commit failure. Keep unrelated work intact and do not stage the whole workspace.
Then call `present_output` with `type: "media"` and `studioCanvasPath` as specified
in the common runtime. All final items must fit the 100 MiB per-item limit.
Do not remove requested content to fit it. Delivery failures resume delivery,
never paid generation. The result card provides preview and downloads; keep
the final chat reply short.
