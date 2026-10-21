---
title: Errors
summary: The one error envelope, the full code vocabulary, and what to do about each
---

Every failure uses one envelope. Every code in this document is emitted by the
server, and every code the server emits is in this document — a test diffs the
two in both directions, so a code cannot be added to the API without appearing
here, and a code cannot be documented without existing.

```json
{
  "detail": "human-readable explanation",
  "code": "snake_case_code",
  "requestId": "0197f590-10b8-7cc0-a6c6-160ed7a18b2c"
}
```

**Branch on `code`, never on `detail`.** `detail` is prose written for a human
reading a log; it may be reworded or localized without notice. `code` is the
contract.


`requestId` echoes the `X-Request-ID` header and is `null` only when the
middleware chain was bypassed. It appears verbatim in the server log line for
the same request — quote it in a bug report.

## How to read the tables

The third column is a **policy**, not a description. It says what a client — or
an agent acting on its own — should do next. Where it says *do not retry*, a
retry cannot succeed and will only amplify load.

Two structural rules explain most of the vocabulary:

- **`400` is malformed, `422` is semantically invalid.** Unparsable JSON is
  `400`; a well-formed body that violates a rule is `422`.
- **A resource you may not see is `404`, never `403`.** Cross-company access is
  indistinguishable from nonexistence by design, so a caller cannot probe for
  the existence of another company's data. `403` is reserved for cases where
  the caller's identity is known to be insufficient for an action *within* a
  company they can already see.
- **A malformed path id is `404`, never `400` or `422`.** An id that cannot
  name a row is indistinguishable, to the caller, from one that names
  nothing — and answering differently would disclose which failures are
  structural. Each resource reports its own not-found code.

---

## Project canvas and native documents

| Code | HTTP | What to do |
|---|---|---|
| `canvas_not_found` | 404 | Canvas, source or target is unavailable. Refresh authorized state; do not probe other Projects or Companies. |
| `canvas_forbidden` | 403 | The current membership cannot perform this action. Preserve unsaved edits and request the needed permission. |
| `canvas_invalid` | 422 | Correct the operation or unsupported document content before retrying. |
| `page_invalid_insert` | 422 | Correct the inserted native block or its structural parent. Headings and paragraphs cannot contain blocks; omit parentId for document-level siblings and position them with afterId or beforeId. Retry the corrected operation. |
| `canvas_conflict` | 409 | Refresh and reconcile changed revisions. For a rejected structural document edit, export the local draft before explicitly reloading; do not silently discard or force-overwrite it. |
| `canvas_limit` | 422 | Reduce document/layout/payload size or split the content; do not retry the same oversized operation. |

## 400 — Malformed request

| Code | Meaning | What to do |
|---|---|---|
| `analytics_busy` | The Analytics read exceeded its bounded database time budget | Retry the same request shortly |
| `invalid_request` | Body is not JSON, is not an object, or carries unknown fields | Fix the request. **Do not retry** unchanged |
| `refresh_token_required` | Refresh call carried no refresh credential | Sign in again |
| `verification_code_invalid` | Email verification code does not match | Prompt for the code again; `429` follows after repeated failures |
| `password_reset_invalid` | Reset link is malformed, already used, or expired | Start a new reset. **Do not retry** |
| `oauth_flow_invalid` | OAuth state does not match a live flow, or the flow expired | Restart the OAuth flow from the beginning |
| `desktop_handoff_invalid` | Desktop handoff code does not match a live handoff | Request a new handoff code |
| `mailbox_webhook_invalid` | The inbound-email callback body or provider signature is invalid | The provider must send a newly signed valid event. **Do not retry** the same invalid request |

## 401 — Not authenticated

| Code | Meaning | What to do |
|---|---|---|
| `authentication_required` | No credential presented | Sign in. **Do not retry** without one |
| `session_invalid` | Session expired, rotated, or replayed | Refresh once; if that fails, sign in |
| `invalid_credentials` | Wrong email or password | Prompt again. Never retry programmatically |
| `oauth_identity_invalid` | The provider's identity assertion did not verify | Restart the OAuth flow |
| `oauth_provider_denied` | The user declined at the provider | Return to sign-in. This is not an error to report |

## 403 — Authenticated but not permitted

| Code | Meaning | What to do |
|---|---|---|
| `company_permission_required` | The caller's Company role lacks the permission the route needs (see `CompanyPermission`) — including a Task handoff, an assignment to another member or a move out of `in_review` by someone who is neither the Task's assignee nor a `tasks.assign` holder, and stopping a Run by someone who is neither its authorizer (whose computer runs it) nor a `tasks.assign` holder | Gate the action on the `permissions` the Company read returned rather than surfacing the error |
| `human_actor_required` | A request carrying a Run proof (`X-Zenith-Team-Run-Token`) reached a route that does not accept a Run. Every route says whether it does; one that says nothing accepts none. The server answers before it checks the session, so this comes ahead of any `401` or `404`. Agents act for their member but may not decide (approve, reject or cancel an Approval; answer or withdraw a Question), edit or start a Task, read the Company-wide dependency graph, create a Project, invite, remove, promote, transfer or delete, nor change who may see a Project (grants, Join, visibility), its files or archive, nor read the Managed projects view or the Project access report | Take the action from the app as a person. **Do not retry** from a Run |
| `run_proof_required` | The daemon's own session (`POST /auth/daemon-session`) reached a route off the daemon plane without a live Run proof of its person. A daemon session acts only on `/api/v1/daemon/*` and, beside exactly one `X-Zenith-Team-Run-Token` whose Run is live and works for the same person, on the routes that accept a Run. A blank, repeated, ended or foreign proof is refused the same way. It is never its person, so every person route refuses it, and it cannot start another daemon session | Send the request from the Run channel with the Run's proof, or from the person's own session. **Do not retry** with the same request |
| `task_assignee_required` | Only a Task's assignee may start its Run: the Run executes on the assignee's own computer (ADR 0014) — no company role overrides it | Show "only {assignee} can start this task; it runs on their computer"; offer "Reassign to me" to `tasks.assign` holders |
| `task_outside_run_scope` | A Task read sent with `X-Zenith-Team-Run-Token` named a Task that is not the Run's own: a Run reads only the Tasks its Agent does for the member it works for. The member could read the Task; the Run cannot. The one exception is the root Agent's Run in its member's direct chat with Company planning on, which reads every Task its member reads. The body carries nothing of the Task — no content, label, member or Agent name — and its `detail` is always "Permission denied. You can only access your own Tasks." A create sent with the token gets it too when the Task would be another member's, a Task Run's in another Project, or in a Project the member cannot read (a read of such a Project or its Tasks is `404` instead, as for the member). It also refuses a Task Run's reads and creates outside its Task chat's Project (ADR 0017), even the member's own Tasks elsewhere. A Run tagged in a channel or group DM uses the access of the member who tagged the Agent, like any other Run; no one else in the chat narrows it. It also refuses a Run's own reads — its Run, its chat, that chat's messages and transcript — when the path names another Run or chat; a Run tagged in a channel or group DM reads its own Run but none of the chat reads | An Agent tells the person, briefly, that it cannot access that Task and that only its assignee can start it, and does none of the Task's work. **Do not retry**; the Task's assignee starts it on their own computer |
| `agent_creator_required` | Only the Agent's creator, or a Company owner or admin, may configure it — update, archive or restore — and only `agents.manage` holders create the root Agent (ADR 0013); every active member creates other Agents and reads them all | Show the Agent read-only for other members and name who can change it |
| `company_access_required` | Route needs a resolved company scope | Send the company-scoped path |
| `account_disabled` | The account is disabled | Stop. Contact support; retrying cannot help |
| `oauth_email_unverified` | The provider reports the email as unverified | Verify at the provider, then retry the flow |
| `csrf_invalid` | Cookie request without a matching `X-CSRF-Token` | Re-read the `zenith_team_csrf` cookie and resend. Bearer clients never see this |
| `origin_forbidden` | `Origin` is not in the allowlist | Configuration problem, not a user problem. **Do not retry** |
| `agent_mail_forbidden` | The resolved live Run does not authorize this Agent Mail operation for the path Conversation | Re-read the Run; do not substitute another identity |
| `forbidden` | A composite result action is unavailable to this Company member | Hide the action unless the caller has owner access |

## 404 — Missing, or not yours

Every code here but `route_not_found` means *either* "does not exist" *or*
"exists in a company you are not a member of". The two are deliberately
indistinguishable.

| Code | Meaning | What to do |
|---|---|---|
| `company_not_found` | Unknown company, caller is not an active member, or a malformed `companyId` — the path-id rule above | Drop the company from local state |
| `work_plan_not_found` | Company Plan context or its Company is missing or inaccessible | Refresh Company access and reload the plan |
| `agent_not_found` | | Refresh the agent list |
| `task_not_found` | | Refresh the task list |
| `task_merge_claim_not_found` | No eligible merge job exists for this runtime, or the authenticated daemon does not own it. A manual merge also answers it when the daemon's member cannot read the Task's Project, as a missing Task does | Drop the claim and poll again |
| `conversation_not_found` | | Refresh the conversation list |
| `run_not_found` | Unknown run, foreign-company run, another creator's direct-conversation run, or (daemon plane) a run whose runtime another daemon or another member registered | Refresh the run list |
| `runtime_not_found` | Unknown runtime, a runtime outside the company in the request path, another member's runtime (including a Connector or Plugin catalog's `runtime_id`), or (daemon plane) a runtime registered by another daemon or another member | Refresh the runtime roster |
| `model_discovery_not_found` | Unknown, expired, or cross-runtime model-discovery request | Start a new discovery if the runtime still exists |
| `skill_not_found` | A requested run-scoped skill is absent from the frozen run context | Re-read the run context; do not substitute a different skill |
| `workflow_not_found` | The Workflow key is not in the catalog this release ships | Reload the Workflow catalog; the Workflow may have been renamed or retired |
| `attachment_not_found` | | Drop the reference |
| `company_resource_not_found` | | Refresh the resource list |
| `inbox_item_not_found` | | Refresh the inbox page |
| `schedule_not_found` | Unknown Schedule, malformed `scheduleId`, or a Schedule outside the Company | Refresh the Schedule list |
| `connector_not_found` | Unknown Connector, or one outside the active Company/provider catalog | Refresh the Connector catalog |
| `plugin_not_found` | Unknown Plugin, one outside the active Company/provider catalog, one the selected Runtime has not observed, or no Runtime of the caller's to read it on (with no `runtime_id`, the default is a Runtime carrying a binding of an unarchived Agent) | Refresh the Plugin catalog |
| `mailbox_message_not_found` | Unknown retained inbound email or attachment | Refresh the Company Email list |
| `agent_mail_draft_not_found` | Unknown Agent Mail draft, or one outside this Conversation | Refresh the draft groups |
| `verification_not_found` | No live email verification for this address | Start a new verification |
| `not_found` | A composite result resource is missing or outside the caller-visible Company scope | Refresh the Run result or Deliverable list |
| `route_not_found` | No registered route matches this method and path: the path is unknown, the API version is wrong, or the method is not served on that path. The one 404 about the URL rather than a resource | Fix the URL or upgrade the client; do not retry unchanged |

## 409 — Conflict

The largest group, and the one where the policy matters most. **A `409` is
never retried blindly.** Re-read the current state, re-apply the intent, and
write again with the new `revision`.

| Code | Meaning | What to do |
|---|---|---|
| `message_skill_unavailable` | A selected message skill changed, is unavailable, or conflicts by name | Select skills again |
| `message_connector_unavailable` | A selected plugin is unavailable for a tagged Agent on your Runtime | Change the Agent/plugin selection or reconnect |
| `agent_revision_conflict` | `expectedRevision` did not match | Re-read, re-apply, write again |
| `agent_skill_daemon_upgrade` | 409 | Update the Zenith Team daemon to load custom skills. |
| `agent_skill_invalid` | Invalid portable SKILL.md, unsafe or missing package files, or import size exceeded | Correct the package and preview it again |
| `agent_skill_not_found` | Skill, frozen version or unexpired import is unavailable to the caller | Refresh the Agent or import again |
| `agent_skill_conflict` | Installed name or expected skill revision conflicts | Refresh and explicitly update the existing skill |
| `agent_skill_unavailable` | Skill storage or the public GitHub source is unavailable | Retry; private repositories can be uploaded as ZIP |
| `agent_skill_limit` | Agent exceeds 32 custom skills or 20 imports in 24 hours | Remove unused skills or wait before importing again |
| `task_conflict` | Task mutation lost a race, violates current state, or reused a task-failure idempotency key with another reason | Re-read; for a live Run failure, replay only the identical reason and key |
| `task_result_selection_required` | A Task with deliveries has no selected result set | Select the exact deliveries before completing the Task |
| `task_result_application_required` | Required suggestions in the selected result set remain unapplied | Review and accept the required changes, or explicitly select a revised result set |
| `conversation_model_settings_forbidden` | Only the Task assignee with execution permission may change its chat model | Ask the assignee to change this chat’s model |
| `conversation_conflict` | Conversation mutation lost a race. **Also returned when a `agentId` is unknown, foreign, or archived** — the insert resolves the agent as a join and a no-row insert is indistinguishable from an id collision — **and when a send resolves no Agent to answer**, because every conversation item belongs to a run | Validate the agent against the agent list before deciding this was a race; check `agent` on the conversation read before offering a composer |
| `conversation_initialization_pending` | This exact conversation belongs to a receipt whose company research has not been started; no new message or queued prompt was written | Start the research (`POST /companies/{id}/initialization`); exact send replays remain valid, and new sends become available as soon as the research Run exists |
| `company_resource_conflict` | | Re-read, re-apply |
| `work_plan_conflict` | | Re-read, re-apply |
| `inbox_item_revision_conflict` | | Re-read the item, re-apply |
| `run_queue_conflict` | Queue changed under the write | Re-read the queue |
| `run_usage_conflict` | An immutable `(runId, provider, model)` fact already exists with different canonical content | Investigate the mismatch; never overwrite the stored fact |
| `runtime_offline` | Model discovery requires the exact runtime to be online | Wait for a heartbeat, then retry initiation |
| `model_discovery_not_running` | A daemon reported a result before the request was atomically claimed | Send a heartbeat and report only claimed work |
| `runtime_remove_blocked` | The runtime is still online | Tell the person to quit Zenith Team on that computer, or stop its zen daemon, then remove it again |
| `agent_binding_required` | The acting member has not set this Agent up on one of their own runtimes (ADR 0014): a Task Run needs the assignee's binding of its executing Agent, a direct chat the sender's, a channel or group-DM tag the tagger's, a Schedule its creator's (preview, create, update, resume and Run now) | Offer the setup step — pick one of the member's runtimes of the Agent's provider — then retry |
| `agent_archived` | The Agent is archived and cannot be set up on a runtime | Restore the Agent first |
| `task_handoff_confirmation_required` | The Task has run before, and changing its assignee or executing Agent cancels the live work and starts the next Run from a fresh workspace; the request carried no `confirmHandoff` and nothing changed. **The body carries `runLive`** flat on the envelope: `true` when a Run of the Task is live right now and confirming cancels it, `false` when the Task only ran before; the `detail` words the same answer for a person | Show what the handoff cancels from `runLive` — never guess it from the Task's `status` — and resend with `confirmHandoff: true` |
| `company_create_conflict` | The globally unique company slug is taken | Choose a different slug |
| `company_delete_conflict` | The Company changed after deletion was confirmed | Refresh Company settings and confirm again |
| `company_profile_revision_conflict` | `expectedRevision` did not match; body carries `currentCompany` | Rebase on `currentCompany`, re-apply, write again |
| `initialization_conflict` | The first company research request did not name the founder's CEO `conversationId` | Send the CEO conversation id with the first request |
| `initialization_model_unavailable` | Luna was not advertised by this runtime | Refresh model discovery on the connected Codex runtime |
| `proactive_mode_conflict` | `expectedRevision` did not match the current Proactive Mode setting | Re-read the setting, re-apply, write again |
| `proactive_mode_conversation_required` | Proactive Mode cannot be enabled without the owner's active direct Conversation with the root CEO Agent | Open or restore that Conversation, then retry with the current revision |
| `profile_revision_conflict` | `expectedRevision` did not match; body carries `currentUser` | Rebase on `currentUser`, re-apply, write again |
| `agent_create_conflict` | A second active root agent was requested | Archive or reuse the existing root |
| `email_already_registered` | | Offer sign-in instead of sign-up |
| `oauth_account_conflict` | The provider identity belongs to another account | Sign in with the original method and link |
| `account_conflict` | Account lifecycle transition is not legal from the current state | Re-read the account |
| `session_already_rotated` | The refresh token was concurrently exchanged without the same `rotationId` | Retry **once** with the current credential; an exact retry carrying the original `rotationId` returns the committed rotation |
| `agent_archive_blocked` | The agent still has active dependents | Reassign or archive the dependents first |
| `agent_reports_to_cycle` | The reports-to edge would create a cycle | Pick a different manager. **Do not retry** |
| `root_agent_topology_immutable` | The root agent's position cannot change | **Do not retry** |
| `task_dependency_blocked` | Execution is blocked by an incomplete prerequisite | Complete the prerequisite; do not poll |
| `task_report_agent_busy` | The direct Conversation's recipient Agent already has an active Run | Wait for the active Run to finish, then preview again |
| `task_report_no_pending` | No pending Task outcome the caller reads remains to send (reports about Projects the caller cannot read stay queued) | Refresh the preview; there is nothing to retry unchanged |
| `task_attachment_unavailable` | A file named in `attachmentIds` is not the creator's finished upload in this Company, or is already attached elsewhere; nothing was created | Upload the file again, then create the Task |
| `task_merge_conflict` | A merge result no longer matches the claimed Task generation or its durable terminal result, or a manual merge names a stale review. A member who may not run Tasks in the Project gets `403 project_permission_required` instead, asked first | Stop retrying unchanged; re-read or claim again |
| `schedule_version_conflict` | `expectedRevision` did not match the Schedule | Re-read, re-apply, write again |
| `schedule_review_stale` | The reviewed Agent, provider, trigger, or Connector selection changed after preview | Preview again and confirm the new digest |
| `schedule_run_active` | The Schedule already has an active Run | Wait for it to finish before mutating or firing again |
| `schedule_terminal` | A completed or cancelled Schedule cannot perform this operation | Stop; create another Schedule if new work is intended |
| `schedule_invalid_state` | The Schedule's current state does not allow this lifecycle operation | Re-read the Schedule and offer only valid actions |
| `schedule_conflict` | Schedule creation, replay, or reviewed state conflicts with current durable state; on preview, the selected Agent or a source cannot be reviewed (unknown, archived, outside the Company, or not an enabled reviewed Connector); on Run now, the Schedule's Agent was archived or otherwise became unusable before the Schedule was paused for it. An Agent the creator has not set up is `agent_binding_required` instead | Re-read or preview; never retry blindly. On preview, pick another Agent or source |
| `connector_provider_unavailable` | None of the caller's Runtimes serves the selected Connector provider, or the `runtime_id` names the caller's Runtime of the other provider | Wait for the Runtime or choose an available provider |
| `connector_conflict` | Connector policy or replay state changed under the operation | Refresh the Connector catalog and re-apply intent |
| `connector_version_exhausted` | The Connector policy revision can no longer advance safely | Stop and surface the `requestId`; operator repair is required |
| `connector_discovery_conflict` | A daemon discovery generation conflicts with the Runtime's stored generation: a publish that is not a replay of it carries an observation no later than the stored one, or a reaffirm names a generation or digest that is not the Runtime's current one (or the Runtime has none) | Publish the full snapshot under a new generation; the daemon does this automatically after a refused reaffirm |
| `plugin_provider_unavailable` | None of the caller's Runtimes serves the selected Plugin provider, or the `runtime_id` names the caller's Runtime of the other provider | Wait for the Runtime or choose an available provider |
| `plugin_discovery_conflict` | A daemon Plugin discovery generation conflicts with stored state | Re-read/rebuild discovery from the current generation |
| `agent_mail_company_email_required` | Company Email is not configured, so Agent Mail has no sender | Configure Company Email, then recreate the draft group |
| `agent_mail_draft_conflict` | A draft revision, group selection, or Run replay differs from durable state | Refresh the draft group and re-apply intent |
| `mailbox_already_configured` | The Company already has its one mailbox | Read the existing mailbox; do not create another |
| `mailbox_address_unavailable` | The requested Company Email local part is already reserved | Choose another address |
| `run_queue_full` | The conversation's queue is at capacity | Wait for a run to drain, then retry |
| `run_not_preparing` | Daemon reported preparation for a run not in that state | Re-read the run's status. Usually a replayed request |
| `run_prepare_lease_expired` | The prepare lease is no longer extendable — the claim lapsed or the run moved on | Stop preparing; re-claim on the next poll |
| `run_not_startable` | The run is not in a startable state | Re-read the run's status; usually a lost race with a cancel or sweep |
| `run_wait_unavailable` | The run cannot wait for a local directory or Studio sign-in in its current state | Re-read the run's status; do not retry unchanged |
| `run_transcript_head_mismatch` | A terminal callback did not prove the Run's current committed transcript prefix | **The body carries the server's own head** (`runId`, `throughSeq`) flat on the envelope. Adopt it, replay any unacknowledged item through the `(runId, itemKey)` fence, and re-send. Never invent a head, never re-send the stale proof |
| `run_terminal_item_invalid` | Completion nominated an item that is not this Run's newest committed `agent_text` | No head to reconcile against: re-read the transcript. Treat the provider result as failed rather than nominating a different item |
| `attachment_conflict` | Attachment state does not allow the transition | Re-read the attachment |
| `attachment_upload_incomplete` | Referenced before its upload completed | Complete the upload, then reference it |
| `approval_required` | The file result has not been approved for publication | Approve the Deliverable, then publish it |
| `conflict` | A composite result, Deliverable, or publication changed or no longer permits the operation | Re-read the resource before retrying |

## 410 — Gone

The resource existed and its window has closed. **Retrying is always wrong**;
start the flow again.

| Code | Meaning | What to do |
|---|---|---|
| `verification_code_expired` | | Request a new code |
| `desktop_handoff_expired` | | Request a new handoff code |
| `attachment_upload_expired` | The upload window elapsed | Initiate a new upload |
| `mailbox_message_expired` | The provider retention window closed for this inbound email | Remove it from local state; the content cannot be fetched again |

## 413 / 415 — Transport limits

| Code | Status | Meaning | What to do |
|---|---|---|---|
| `request_too_large` | 413 | Body over the cap | Reduce the payload; for files use the attachment flow |
| `unsupported_media_type` | 415 | `Content-Type` is not `application/json` | Set the header |

## 422 — Well-formed but invalid

| Code | Meaning | What to do |
|---|---|---|
| `validation_failed` | A field violates a rule, or names one that cannot change (an Agent's `provider`). Also returned for unknown, repeated, or empty-valued query parameters | Fix the request. **Do not retry** unchanged |
| `agent_skill_frontmatter_invalid` | Missing delimiters, malformed or unsupported YAML frontmatter, or compatibility over 500 characters | Use supported YAML fields between `---` delimiters and keep compatibility within 500 characters |
| `agent_skill_name_required` | Missing or blank skill name | Add a non-empty `name` field; supported names are normalized during preview |
| `agent_skill_description_invalid` | Missing, blank, overlong or invalid skill description | Add a plain-text `description` of at most 1024 characters without control characters |
| `agent_skill_reference_invalid` | Malformed local link or a reference outside its skill folder | Correct links and keep local targets inside the skill folder |
| `agent_skill_reference_missing` | A linked local file is absent | Include linked files in the skill folder; use ZIP when uploading supporting files |
| `agent_skill_archive_invalid` | ZIP or archive entry cannot be read | Export a fresh ZIP and preview it again |
| `agent_skill_files_invalid` | Unsafe or colliding paths, hidden or provider files, shortcuts or special files | Use unique relative paths and remove unsupported files |
| `agent_skill_package_limit` | Package exceeds a file-count or size limit | Keep uploads within 32 MiB, expanded packages within 128 MiB, SKILL.md within 1 MiB, supporting files within 16 MiB, and archives within 2000 files and 4000 entries |
| `agent_skill_instructions_invalid` | SKILL.md is empty or contains invalid UTF-8 or NUL bytes | Save SKILL.md as non-empty UTF-8 text |
| `agent_skill_count_invalid` | Source contains zero or more than 32 skills | Include 1 to 32 skill folders, each with a file named SKILL.md |
| `invalid_cursor` | Cursor is malformed or uses an unsupported encoding version | Restart pagination from the first page with no cursor |
| `invalid_multipart` | Attachment upload was not exactly metadata-then-file | Rebuild the multipart body |
| `attachment_integrity_mismatch` | Uploaded bytes do not match the declared size | Re-upload. **Do not** retry the complete call alone |
| `client_transport_mismatch` | The credential's transport does not match the request origin — e.g. a bearer client on a cookie flow | Use the transport the login response declared |
| `agent_mail_invalid_recipients` | A delivery has an invalid, repeated, or over-limit recipient set | Correct the recipients before sending |
| `agent_binding_provider_mismatch` | The runtime named for a binding, or as a removal's target, runs another provider than the Agent's | Offer only runtimes of the Agent's provider. **Do not retry** unchanged |
| `target_unavailable` | A presented result item no longer resolves to verified stored content | Re-present the result with available items |
| `output_not_publishable` | The primary output is not a verified ZIP archive | Present a verified ZIP as the primary file, complete the Run, and obtain any required Task Deliverable approval before publishing |

## 429 — Rate limited

| Code | Meaning | What to do |
|---|---|---|
| `verification_resend_too_soon` | Resend requested inside the cooldown | Wait until `resendAvailableAt` from the last response |
| `verification_code_locked` | Too many wrong codes — the flow's attempt budget is exhausted | Stop. The lock is **permanent** for this flow — waiting does not clear it, and resending cannot either. Open a new verification flow |
| `agent_mail_send_limit_exceeded` | Sending would exceed the Company's rolling Agent Mail recipient limit | Wait for the reported window to clear, then retry the reviewed draft |
| `mailbox_provider_rate_limited` | The inbound-email provider throttled a content or attachment read | Wait for `Retry-After`, then retry |

## 500 / 502 / 503 — Server and dependency

Transient unless stated. Back off exponentially; do not hot-loop.

| Code | Status | Meaning | What to do |
|---|---|---|---|
| `internal_error` | 500 | Unexpected failure | Retry once with backoff, then surface the `requestId` |
| `oauth_provider_unavailable` | 502 | The upstream provider failed | Retry with backoff, or offer another sign-in method |
| `service_not_ready` | 503 | The server is starting or a dependency is down | Retry with backoff |
| `email_delivery_unavailable` | 503 | Verification mail could not be sent | Retry; the account is unaffected |
| `password_reset_unavailable` | 503 | Reset delivery is down | Retry with backoff |
| `oauth_not_configured` | 503 | This deployment has no OAuth provider configured | **Do not retry.** Hide the button |
| `attachment_storage_unavailable` | 503 | Object storage is unreachable | Retry with backoff |
| `attachment_unavailable` | 503 | The attachment service cannot safely process the current attachment state | Retry once; if it persists, surface the `requestId` |
| `schedule_request_failed` | 500 | An unexpected Schedule operation failed | Retry once with backoff, then surface the `requestId` |
| `connector_request_failed` | 500 | An unexpected Connector catalog or mutation operation failed | Retry once with backoff, then surface the `requestId` |
| `plugin_request_failed` | 500 | An unexpected Plugin catalog or mutation operation failed | Retry once with backoff, then surface the `requestId` |
| `mailbox_webhook_failed` | 500 | A valid inbound-email callback could not be durably accepted | The provider may retry with backoff using its same event id |
| `connector_action_unavailable` | 503 | The active Runtime cannot currently execute the Connector action | Wait for Runtime recovery, then refresh before retrying |
| `plugin_action_unavailable` | 503 | The active Runtime cannot currently execute the Plugin action | Wait for Runtime recovery, then refresh before retrying |
| `mailbox_provider_unavailable` | 503 | The inbound-email provider is temporarily unavailable | Retry with backoff |
| `deliverable_unavailable` | 500 | A Deliverable operation failed unexpectedly | Retry once with backoff, then surface the `requestId` |
| `publication_subdomain_taken` | 409 | This name is already reserved by an Output | Choose another name |
| `publication_subdomain_disabled` | 503 | Custom URL routing is not configured for this publication | Restore its original Cloudflare routing configuration |
| `publication_disabled` | 503 | This deployment has no publication provider configured | **Do not retry.** Hide publication actions |
| `publication_unavailable` | 500 | A publication operation failed unexpectedly | Retry once with backoff, then surface the `requestId` |

---

## Conflicts that carry state

Three endpoints deliberately extend the envelope so a client can resolve without
a second round trip. The extra key sits alongside `detail`/`code`/`requestId`:

| Endpoint | Extra key |
|---|---|
| `PATCH /api/v1/me/profile` | `currentUser` |
| `PATCH /api/v1/companies/{companyId}/profile` | `currentCompany` |
| `PATCH /api/v1/companies/{companyId}/tasks/{taskId}` (`task_handoff_confirmation_required` only) | `runLive` |

Treat `currentUser` and `currentCompany` exactly as if they had come from the
resource's `GET`. `runLive` is a boolean: whether confirming the handoff
cancels a Run of the Task that is live right now.

## Codes this contract renames

Two Inbox codes were bare nouns that collided with every other resource's
failures. Inbox endpoints now name them after their resource; composite result
surfaces may still use the shared codes documented above.

| Was | Is |
|---|---|
| `not_found` (inbox) | `inbox_item_not_found` |
| `conflict` (inbox) | `inbox_item_revision_conflict` |

## Human-in-the-loop codes

Served by [approvals and questions](approvals.md), including the supplemental
live Run proof used when an agent raises either resource.

| Code | Status | Meaning | What to do |
|---|---|---|---|
| `approval_not_found` | 404 | Unknown approval, or one in another company | Refresh the approvals list |
| `approval_resolver_forbidden` | 403 | The caller does not satisfy the request's resolver policy | Hide the action for this caller |
| `approval_conflict` | 409 | Already resolved, or `expectedRevision` did not match | Re-read; **do not retry blindly** |
| `approval_expired` | 410 | The TTL elapsed; no action executes | **Do not retry.** The lifecycle worker resumes the agent with expiry context |
| `question_not_found` | 404 | Unknown question, or one in another company | Refresh the questions list |
| `question_resolver_forbidden` | 403 | Caller does not satisfy the resolver policy | Hide the action |
| `question_conflict` | 409 | Already answered, or revision mismatch | Re-read the question |
| `question_expired` | 410 | The TTL elapsed | **Do not retry.** The lifecycle worker resumes the agent with expiry context |
| `run_token_invalid` | 401 | The supplemental Run token is malformed, expired, unknown, cross-company, or its Run is no longer running; or the session it was sent with is not the member the Run works for (its authorizer, ADR 0014, 0017) | Stop the agent tool call; a new live Run is required |

## Company Wiki

| Code | HTTP status | Meaning |
| --- | --- | --- |
| `wiki_page_not_found` | 404 | Page is missing or outside the authorized Company. |
| `wiki_collection_not_found` | 404 | Collection is missing or outside the authorized Company. |
| `wiki_page_conflict` | 409 | A page has changed since the caller read it. Reload before saving. |
| `wiki_page_archived` | 409 | Archived page cannot be edited or favorited. |
| `wiki_collection_conflict` | 409 | Collection name is already used or its revision is stale. |
| `wiki_artifact_ref_unavailable` | 422 | A newly added `[[artifact:…]]` citation names an Artifact or item that is missing or that the author cannot open. One code for all three, so a citation cannot probe what exists. Citations already in the page, or in a restored revision, are kept. |
| `wiki_artifact_ref_limit` | 422 | The body cites more than 50 distinct Artifacts or items. |
| `wiki_agent_access_disabled` | 403 | Company Wiki reading is switched off for the Run's Agent (its `wiki` capability group is disabled). Enabling the group in the Agent's capabilities restores access. The same code refuses a company-research Run's Wiki write when the group is off. |
| `wiki_agent_write_forbidden` | 403 | The Run may not write the Company Wiki: only the founder's own `company_research` Run, while `running`, may. Any other Run, a finished Run, or a Run authorized by another member gets this code. |
| `wiki_agent_write_limit` | 422 | The write would give the Run more than 40 Wiki pages. Rewriting a page it already owns is still allowed. |

## Projects

| Code | HTTP status | Meaning |
| --- | --- | --- |
| `project_not_found` | 404 | Project not found within authorized Company scope, or the caller can neither read it nor manage it (ADR 0017). The two are the same answer on purpose. A Task list filtered by a Project the caller cannot read answers it too. So does an Analytics read filtered by one, and a Task create naming one. On a Task list or create, reading is what counts: an owner or admin who manages the Project through their role but cannot read it gets this answer too, since they can open no Task there. |
| `project_conflict` | 409 | Stale revision or duplicate Project key. |
| `project_archived` | 409 | Archived Project cannot be edited or admit Task work. |
| `project_open_tasks` | 409 | Archive refused: the Project still has a Task outside `done`/`cancelled`, a nonterminal Task Run, or a pending or executing merge. |
| `project_permission_required` | 403 | The caller can see the Project, but their access to it (ADR 0017) lacks the permission the action needs. The response names no one and no other permission. On a Task Run start the detail is a few plain words naming the Agent ("Eli didn't start: you can't run tasks here.", or "the assignee can't…" for anyone else), never the Project. Someone holding `project.grants.manage` on the Project can grant the permission; do not retry unchanged. A Project the caller cannot see answers `project_not_found` instead. |
| `project_member_required` | 409 | A grant named someone who is not an active Company member with an enabled account. On a Task create or handoff: the assignee cannot read the Task's Project; the detail names nobody. Give them access first or pick someone who reads it; do not retry unchanged. |
| `project_grant_revision_conflict` | 409 | The Project's access changed since the caller read it (`access.revision`). Reload the Project and its grants, then decide again. |
| `project_grant_exceeds_own` | 403 | The self-grant cap. The caller manages the Project's access only through their own grant (not through the Company permission `projects.manage`), and the set asked for holds a permission they do not hold on the Project, or the person's own stored grant being changed or removed does. Only that stored grant counts, never what the person also holds through their Company role. The response names no permission. Owners and admins are not capped, and Join is not checked. Ask an owner, an admin or someone with at least as much access; do not retry unchanged. |
| `project_permission_unknown` | 422 | A person grant, a Group grant or either grant preview names a permission outside the catalog. The detail names it, for example `Unknown Project permission "project.results.review"`: migration 000102 removed that one, and a desktop built before it still sends it in its Approve and Manage presets. Update the client, or send only catalog permissions; do not retry unchanged. |
| `project_visibility_floor` | 409 | On a Project open to the Company, every member keeps `project.read`, `project.tasks.write` and `project.tasks.execute` without a grant, so a grant only adds on top of them and nobody can be removed (a `DELETE` or an empty `PUT`). Make the Project restricted first. |
| `no_eligible_resolver` | 409 | A Task Run raised an approval or question that no reader of the Task chat's Project may decide (`project.approvals.resolve`; a Company Plan Approval also needs `project.tasks.write`, which approving it requires). Nothing is stored and the Run fails with `failureReason` `no_eligible_resolver`, which is never retried; the member it worked for gets a `run_failed` Inbox item (ADR 0017). Grant the permission to someone who reads the Project, then reply in the Task chat. |

## Groups

| Code | HTTP status | Meaning |
| --- | --- | --- |
| `group_not_found` | 404 | No such Group in the Company, or a malformed `groupId`, on a Group route or a Project's Group grant route. |
| `group_conflict` | 409 | The Group changed since the caller read it (a stale `expectedRevision`, the preview's revision included), or another Group of the Company already has the name, ignoring case. Reload the Group, preview again, then save. |
| `group_member_required` | 409 | Someone named to join a Group is not an active Team member with an enabled account: an Agent's id, a former member or a stranger. The response names nobody. |
| `group_limit_reached` | 409 | A Group cap: 100 Groups in a Company, 500 members in a Group, or 50 Group grants on a Project. |
| `group_grant_permission_required` | 403 | "No laundering": a change of a Group's members, or deleting it, would give or take access on a Project where the caller cannot give or take that access themself (they lack `project.grants.manage` there, or manage it only through their own grant and the Group holds more than they do). The response names no Project. Ask an owner or an admin. |

## Team invitations

| Code | HTTP | Policy |
| --- | --- | --- |
| `team_invitation_not_found` | 404 | Check the link or request a new invitation. |
| `team_invitation_account_mismatch` | 403 | Sign in with the verified invited email. |
| `team_invitation_inactive` | 410 | Refresh the invitation state; request renewal if expired. |
| `team_invitation_revision_conflict` | 409 | Refresh before retrying the owner action. |
| `team_invitation_rate_limited` | 429 | Wait for Retry-After before retrying. |
| `team_invitation_unavailable` | 503 | Configure invitation email delivery before sending. |


## Team membership

| Code | HTTP | Policy |
| --- | --- | --- |
| `team_last_owner` | 409 | Keep at least one active owner; do not retry unchanged. |
| `team_member_revision_conflict` | 409 | Refresh the member roster before retrying. |

| `team_share_link_membership_removed` | 403 | Removed members need a fresh email invitation from an owner or admin. |
| `team_share_link_verification_required` | 403 | Verify the account email before joining through a shared link. |
| `team_share_link_unavailable` | 503 | Invite link encryption or origin is not configured. |

## Studio conversation project binding

| Code | HTTP | Policy |
| --- | --- | --- |
| `studio_project_run_not_found` | 404 | The Run proof, runtime owner, daemon, or frozen Studio selection is unavailable. Reconnect the authorized Run; never substitute another Run or project. |
| `studio_project_invalid` | 422 | Correct the operation fields. Reserve needs a title; bind needs the reservation and project UUIDs. |
| `studio_project_conflict` | 409 | The conversation or project already has a different binding. Keep the existing binding; never overwrite it. |

## Personal Calendar and Social connections

| Code | HTTP | Meaning |
| --- | --- | --- |
| `calendar_not_found` | 404 | calendar not found |
| `calendar_invalid` | 422 | calendar invalid |
| `calendar_conflict` | 409 | calendar conflict |
| `calendar_range_exceeded` | 422 | calendar range exceeded |
| `social_unavailable` | 503 | social unavailable |
| `social_not_found` | 404 | social not found |
| `social_invalid_flow` | 422 | social invalid flow |
| `social_provider_failed` | 502 | social provider failed |
| `social_approval_required` | 409 | Post has no valid human confirmation; review with ask_human before publishing |
| `social_conflict` | 409 | social conflict |
| `social_invalid_post` | 422 | invalid social post or unsupported media |
| `social_reconnect_required` | 409 | reconnect to renew publishing permissions |

## Human discussions

Discussion endpoints reuse strict binder/authentication/cursor errors. Errors carry
no private object or content. JSON reads are private, no-store.

| HTTP | Code | Detail / recovery |
| --- | --- | --- |
| 404 | `discussion_not_found` | Conversation or message was not found. Missing, inaccessible and wrong-type resources share this response. |
| 404 | `huddle_not_found` | Huddle source Conversation was not found or is not accessible. |
| 403 | `huddle_budget_forbidden` | Huddle budget access is not allowed. |
| 409 | `huddle_ai_conflict` | Huddle AI settings changed. Reload and retry. |
| 403 | `huddle_ai_disabled` | Voice Agents are turned off on this server. |
| 503 | `huddle_not_configured` | Calls are not set up on this server (LiveKit is not configured). |
| 429 | `huddle_tool_limit` | Voice lookup limit reached. Try again later. |
| 409 | `huddle_proposal_conflict` | Work proposal changed or is no longer editable. Reload and retry. |
| 429 | `huddle_proposal_limit` | This call has reached its work proposal limit. |
| 409 | `huddle_budget_conflict` | Huddle budget changed. Reload and retry. |
| 409 | `huddle_budget_exhausted` | Huddle AI budget is unavailable or exhausted. |
| 403 | `huddle_operation_forbidden` | This Huddle action is not allowed. |
| 409 | `huddle_request_conflict` | Request key was already used for a different Huddle. |
| 503 | `huddle_unavailable` | Huddle is temporarily unavailable. Try again. |
| 409 | `huddle_already_connected` | You are already in this Huddle in another window or device. Leave there, then join again. |
| 403 | `discussion_operation_forbidden` | This action is not allowed. Current content/management discovery is established first. |
| 409 | `discussion_revision_conflict` | Conversation or message changed. Reload and try again. |
| 409 | `discussion_request_conflict` | Request key was already used for different content. |
| 409 | `discussion_cursor_expired` | Reload this conversation before continuing. Discard cursors after grant/membership changes. |
| 409 | `discussion_read_only` | This conversation is read-only. Matching replay remains available with read access. |
| 409 | `discussion_limit_reached` | The conversation limit for this action was reached. |
| 422 | `discussion_agent_required` | The message tags an Agent that is not in this channel or group chat, or one that is archived. Nothing was written. Offer to add the Agent, then send again. |
| 503 | `discussion_unavailable` | Conversation is temporarily unavailable. Try again. Retry-After: 1; keep the send key after an uncertain commit. |

## Studio editor

| Code | HTTP | Meaning |
| --- | --- | --- |
| `studio_editor_not_found` | 404 | Studio editor not found. |
| `studio_editor_invalid` | 422 | Studio editor invalid. |
| `studio_editor_permission_denied` | 403 | Studio editor permission denied. |
| `studio_editor_generation_conflict` | 409 | Studio editor generation conflict. |
| `studio_editor_binding_conflict` | 409 | Studio editor binding conflict. |
| `studio_editor_origin_conflict` | 409 | Studio editor origin conflict. |
| `studio_editor_account_conflict` | 409 | Studio editor account conflict. |
| `studio_editor_request_conflict` | 409 | Studio editor request conflict. |
| `studio_editor_reauth_required` | 409 | Studio editor reauth required. |
| `studio_editor_upstream_denied` | 403 | Studio editor upstream denied. |
| `studio_editor_source_unavailable` | 404 | Studio editor source unavailable. |
| `studio_editor_runtime_unavailable` | 503 | Studio editor runtime unavailable. |
| `studio_editor_authority_unavailable` | 503 | Studio editor authority unavailable. |
| `studio_editor_unsupported` | 409 | Studio editor unsupported. |
| `studio_editor_rate_limited` | 429 | Studio editor rate limited. |
| `studio_editor_outcome_unknown` | 504 | Studio editor outcome unknown. |
| `studio_editor_recovery_required` | 409 | Studio editor recovery required. |
| `studio_editor_upstream_invalid` | 502 | Studio editor upstream invalid. |

## Whiteboards

| Code | Status | Meaning |
| --- | --- | --- |
| `whiteboard_not_found` | 404 | The Board, or the file requested on it, is unavailable in the current Company. |
| `whiteboard_item_not_found` | 404 | A command names an item or connector the Board no longer has. The Board itself is still available; refresh and review the change. |
| `whiteboard_forbidden` | 403 | Current permissions do not allow this action. |
| `whiteboard_invalid` | 422 | The command, note schema or request is invalid. |
| `whiteboard_request_key_reused` | 422 | The request key was already used for a different request. Send a new change with a new key. |
| `whiteboard_conflict` | 409 | The affected revision changed. Reconcile before retrying. |
| `whiteboard_limit` | 422 | A Board content, note, command or batch size limit was exceeded. Pending work is not saved. |
| `whiteboard_locked` | 409 | The object is locked, so a move or resize was rejected. Unlock it first. |
| `whiteboard_presence_full` | 409 | The Board has as many present sessions as it can show. Presence is skipped; editing is unaffected. |
