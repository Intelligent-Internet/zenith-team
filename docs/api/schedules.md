---
title: Schedules
summary: Reviewed one-shot and recurring work, with replay-safe firing history
---

A Schedule is a server-authoritative definition owned by the Company member
who created it. It points at one Agent and an exact reviewed set of Connector
sources. A Schedule also owns a `schedule` Conversation, so every materialized
firing lands in the same ordered Conversation stream as ordinary work.

Every route is owner-gated and returns `Cache-Control: private, no-store`.
The lifecycle is `active ↔ paused → completed | cancelled`; cancelled
and completed Schedules are terminal.

## Review before write

`POST /companies/{companyId}/schedules/preview` normalizes a draft and returns
its `reviewDigest`. Create, update, and resume echo that digest so a provider,
Agent, or Connector-policy change cannot silently broaden scheduled authority.
The trigger is exactly one of:

- `{ "type": "once", "runAt": "<future UTC minute>" }`
- `{ "type": "cron", "expression": "<five fields>", "timeZone": "<IANA zone>" }`

Cron intervals under five minutes are rejected. `sourceIds` is always present;
an empty array deliberately grants no external Connector. The preview's
`humanSchedule` puts the trigger in plain words, such as
`Every weekday at 09:00 · Asia/Ho_Chi_Minh`; a cron shape with no plain
wording keeps its expression.

Every firing runs on the creator's own binding of the Agent (ADR 0014). A
creator who has not set the Agent up on one of their runtimes gets
`409 agent_binding_required` from preview, create, update, resume, and Run
now; an unknown or archived Agent stays `schedule_conflict` on preview and
Run now and `schedule_review_stale` on a write that echoes a digest.

Create carries `requestKey`, never Schedule or Conversation ids. PostgreSQL
creates both durable ids in one transaction. A new request returns `201`; an
exact replay returns the original Schedule with `200`.

## Firings

`POST .../schedules/{scheduleId}/runs` carries only schema version and a
`requestKey`. PostgreSQL generates UUID firing, Message, and Run ids. An
exact replay returns `200`; a new manual firing returns `201`. Both answer
with the same record the firing history lists, including `messageSeq` and
the Run's current `runStatus`.

A Schedule's `lastRunAt` is its latest firing, timed or Run now. Run now sets
it and changes nothing else on the Schedule: `nextRunAt` and the revision stay,
so an edit or pause sent with the revision you already hold still applies.

Firing history also records non-materialized occurrences. Therefore
`messageId`, `messageSeq`, `runId`, and `runStatus` may be `null` when a firing
was `skipped`, `superseded`, or `needs_attention`. A successful manual firing
is `materialized` and has those identities.

The selected Connector set is checked again when its Run is claimed. It is
frozen in full or the Run is cancelled with `connector_unavailable`; Zenith Team
never executes a Schedule with a partial subset of the reviewed sources.

Schedule and firing lists use opaque seek cursors; a malformed cursor is
`422 invalid_cursor`, never `validation_failed`. Query parameters retain the
API's non-JSON spelling: `preset_key`, not `presetKey`.
