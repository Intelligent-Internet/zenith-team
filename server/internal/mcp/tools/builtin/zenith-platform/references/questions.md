# Founder Questions

All commands accept `--output json`. Ids are UUIDs; the Task may also be
named by its key (`ENG-7`) or its displayed label where a Task id is accepted.

## Questions

```
zen question ask --title "Which plan?" \
  --prompt "Pick the launch plan" \
  --option "Plan A: ship now" --option "Plan B: wait for pricing" \
  [--help-text "..."] [--required] [--multi] [--no-custom-answer]
zen question ask --title "Domain" --prompts-file ./prompts.json
```

Each `--prompt` starts a new prompt; `--option` values attach to the most
recent prompt. Custom answers are allowed unless `--no-custom-answer` is
given. `--prompts-file` takes the exact API `prompts` array for complex
questionnaires. The command prints the pending Question; end the Run after
it. The founder's answer starts a continuation Run.

When the user's request needs Zenith Studio that is disabled or needs connection, request its setup card
with `ask_human` and this single prompt (or pass this array to
`zen question ask --title "Enable Zenith Studio" --prompts-file ./prompts.json`):

```json
[{"id":"studio","prompt":"Connect to Zenith Studio","toolSetup":"zenith-studio","selectionMode":"single","required":true}]
```

End the Run after the request succeeds. Briefly say, in the user's language,
that Studio needs enabling or connecting, matching its actual state. Do not narrate creating
a card, waiting for an answer, or starting another Run. Do not mention Runs or
Questions to the user during Studio setup.

The human enables and connects Studio in the card. Once the exact Agent's
connection is verified, Zenith Team resolves setup and resumes the original
request automatically with the updated Agent capabilities. There is no second
answer or Continue click. This setup request does not grant Studio authority
to the current Run.

For model selection, use an ordinary required single-choice question with
`options` containing the compatible catalog model IDs and display names.
Keep its prompt short; do not substitute a list of model names in a free-text
prompt or ask the human to type a model ID. Omit `toolSetup`; setup metadata
is only for enabling or connecting Studio.

## Social post review

Before publishing any social post, use `social_read` to resolve the user's
account, target and conversation media. Call `ask_human` with a single required
single-select prompt and a `socialPost` object containing `connectionId`,
`targetId`, `text`, and optional `attachmentIds`. Provide choices `publish`
("Publish now") and `cancel` ("Do not publish"), in that order, and set
`allowCustomAnswer: true` so the user can request edits. Zenith Team renders the
account, caption and media as a preview. End the Run and wait for the answer.

Only after the human chooses `publish`, call `social_publish` with the returned
`questionId`. It publishes exactly the reviewed snapshot. A custom answer means
revise the draft and ask again; cancel means stop. Reuse the same question ID
for retries. Never create another review to resend a `publishing` or `unknown`
outcome. Accounts are managed in Settings → Plugins → Social accounts.
