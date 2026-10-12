# Zenith Team video execution

Load this together with `references/zenith-team-runtime.md` for any video
request, including a single clip. Follow `resources/video-agent-mode.md` and
its selected operation, goal, model, stage and review owners. Load only the
next required branch. These Team adapters change execution and delivery;
they preserve the shared creative, identity, continuity and quality contracts.

## References and tools

Read this immutable Run snapshot through `load_skill_reference` with
`skillKey: "zenith-studio-cli"` and a package-relative `path`. Load only the
relevant branch. Do not edit the snapshot, substitute live Agent settings,
or substitute a separately installed skill if a reference cannot be loaded.

In shared references, `factory_read_skill` means `load_skill_reference`.
`factory_*` web tool names describe actions to translate below, not tools that
must be connected in Team. Keep plans, prompt packets, source-ID mappings,
review notes, and local compositions in the Run workspace; reuse existing work
on continuation. Map web `/workspace/...` paths to that workspace instead of
creating those absolute directories or writing into the immutable skill snapshot.

| Shared action | Zenith Team execution |
| --- | --- |
| `factory_video_plan`, `factory_video_step`, prompt-pack and status actions | Read/write the plan, stage checkpoints, and recorded CLI IDs as local JSON/Markdown files. |
| `factory_video_review`, `factory_understand_video`, frame extraction, contact sheets, media probes | Use the Agent's available inspection tools and local non-generative media processing; review the actual files. |
| Catalog, generation, node execution, asset materialization | Use the public CLI as described in [media-generation.md](media-generation.md). |
| `factory_request_user_input` | Use `ask_human` only for required input; preserve the user's existing authorization. |
| HyperFrames preparation, preflight, scene review, render, and Final Output application | Use the local renderer and file delivery described in [assembly.md](assembly.md). |

When required input or setup blocks progress, finish independent work and call
`ask_human` with one consolidated question. After it reports a pending Question,
end the Run; the answer starts a continuation. Report an actual missing local
executable, denied Run permission, CLI error, or unavailable reference precisely.
Missing web Final Output tools are not a setup failure. When composition needs
Chrome, use `hyperframes browser ensure` before paid generation; Node 22+, npm,
FFmpeg and ffprobe must be installed on the Runtime computer.

## Long videos with HyperFrames

Studio generation supplies individual clips, images, narration and music.
For a longer video, load `references/assembly.md` and the relevant bundled
HyperFrames workflow, then build a local composition covering the entire requested
duration. Split the story into timed scenes/chapters, generate only missing media,
and keep stable source IDs, scene timings and progress in the local plan. Resume
that plan and reuse downloaded assets across Runs. Model clip-duration limits do
not limit the assembled video's duration.

Check `hyperframes doctor` and `hyperframes browser ensure` before paid generation.
Use the daemon-managed `hyperframes` command for init, lint, check, inspect,
snapshot and render. It is installed with Studio; no separate Agent switch or
external skill installation is needed. Keep multi-scene HTML, assets, captions,
narration and music under the composition directory. Inspect every scene and seam,
then render the complete timeline, including audio and its ending. Use ffprobe to
verify the exact final duration, dimensions and audio; inspect beginning, middle,
ending and transitions. Do not deliver one short source clip as a finished long video.

The current file-delivery limit is 100 MiB per item. Check the final file size
before `present_output`. If necessary encode the full video at an appropriate
bitrate with FFmpeg (reserve headroom for audio/container overhead), then check
quality and duration again. Never silently truncate the requested video to fit.
Preserve the composition and original render in the workspace. Include the
composition archive as a supporting item when requested and within the limit.

## Publish the reviewed composition

A locally assembled video must be published to this Conversation's Studio Project
before final delivery. Save `output/render.json` containing a meaningful `label`
and ordered, unique `source_node_ids` for the actual Studio images, clips and
audio used. Use an empty list only when the composition uses no Studio sources.
Never invent source IDs, edges or prompts. Check the final video is at most
100 MiB before upload; if necessary re-encode, then review the exact delivery
file again without truncating its duration.

```json
{"label":"Final video","source_node_ids":["REAL_SOURCE_NODE_UUID"]}
```

```sh
zenith-studio --json renders publish PROJECT_ID output/studio-video/renders/final.mp4 --manifest output/render.json
zenith-studio --json canvas export PROJECT_ID --node FINAL_NODE_ID --output-item FINAL_NODE_ID:0 --output output/source-FINAL_NODE_ID.canvas.json
```

Use the `node_id` returned by successful publication as `FINAL_NODE_ID`; export
traverses its recorded source graph. The publish command uploads the reviewed
bytes, creates a native Final Output video with a thumbnail and source edges,
and returns the same node on retry. Its default idempotency key binds the file
and manifest; preserve both when retrying. A changed render creates a new output
and preserves earlier publications. Never rerun paid generation after an upload
failure. Keep the local file and report incomplete publication if retry fails;
do not claim that the video is saved in Studio or silently deliver only sources.
Map the published node to the final video's item ordinal, never a source clip.
Publish and present the same bytes. Retain the local composition for future edits;
uploading the video does not upload its editable HyperFrames project.

Deliver the reviewed generated clip or published composition through
`present_output` with its source canvas, as specified in the common runtime.
For each planned media generation, use the batching and checkpoint lifecycle
in `references/zenith-team-media.md` after the video workflow has determined
the required prompt, inputs, model and parameters. This does not replace the
video planning, composition or review stages.
