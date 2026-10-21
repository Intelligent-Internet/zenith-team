# SQL query rules

- Every Company-owned read or write takes `company_id` and repeats the scope in
  SQL, even when the caller's principal already carries the company.
- Client-generated IDs use an exact-replay `ON CONFLICT` predicate: identical
  immutable input returns the existing row; conflicting reuse returns no row.
- Message append transactions lock the authorized Conversation first, then
  allocate `max(seq) + 1` as a second READ COMMITTED statement, so a lock
  waiter sees a fresh snapshot.
- Every writer of `conversation_items.seq` — the three user-message inserts and
  the agent append — refuses a value past `9007199254740991`
  (`protocol.MaxJSONSafeInteger`) in the statement that allocates it, so no
  reader is handed a seq a JavaScript `Number` would round. Past the ceiling the
  insert writes nothing and returns no row: the ceiling belongs to the query,
  not to whichever caller happens to be writing.
- Run claims are single-statement
  `UPDATE ... WHERE id = (SELECT ... FOR UPDATE SKIP LOCKED)`; recovery and
  sweeper scans use ordered bounded batches.
- One file per domain (`runs.sql`, `runtimes.sql`, `conversations.sql`, ...);
  a query lives with the table it primarily reads.
- Named `... FOR UPDATE` lock queries acquire locks in a fixed documented
  order inside each transaction; events are collected in-tx and published
  only after commit. The order is company -> member -> agent -> group ->
  project -> conversation -> run. A Group lock (`LockCompanyGroupForChange`,
  `LockCompanyGroupForShare`) is never taken after a Project, Conversation
  or Run lock; a Group change locks the Group, then each Project it reaches
  in id order, then sweeps. A Project lock (`LockProjectForAccessRead`,
  `LockProjectForAccessChange`, `GetProjectForUpdate`,
  `GetTaskProjectForShare`, `LockCompanyPlanProjects`, or
  `auth.LockProjectAccess`) is never taken after a Conversation or Run lock in
  the same transaction; `TestProjectLocksPrecedeConversationAndRunLocks` scans
  the Go code for it, one function at a time. A Company Plan Approval raise
  takes the plan's Project and Task chat locks (`LockCompanyPlanScope`)
  between its member locks and its origin chat and Run locks;
  `TestHumanInputMutationLockOrder` pins that order across those calls.
  Code that already holds Conversation or Run locks reads Project facts with
  `GetProjectAccessFacts`, which takes no lock. The Company's Task dependency
  graph lock (`LockTaskDependencyGraph`, an advisory lock) comes after the
  member locks and before any Project lock, in Task writes as in a Company
  Plan apply.
- Project access (ADR 0017) is checked inside the read, through the
  `project_access`, `task_access`, `conversation_readers`,
  `attachment_access` and `output_access` views. `conversation_readers` and
  `attachment_access` are UNION ALL views: Postgres does not push an outer
  join clause into their arms, so a plain `JOIN` or `EXISTS` against them
  builds the person's whole readable set first. A query over a list of rows
  reaches them through a correlated lookup that names both the row and the
  person: `CROSS JOIN LATERAL (SELECT 1 FROM attachment_access AS access
  WHERE access.company_id = … AND access.attachment_id = … AND
  access.user_id = @actor LIMIT 1)`, or a scalar subquery of the same shape.
  `TestProjectViewCorrelatedLookupsStayIndexed` pins that this form uses
  each arm's indexes.
- `project_permission_held(...)` is not inlined by Postgres (its body has an
  EXISTS), so it runs as one opaque call per row. Use it for one write or a
  claim fence such as the merge job claim, where the candidate set is small;
  list predicates join the views instead. Bind its three sets from
  `auth.ProjectPermissionHeldSets()`.
- What a Run or the Huddle voice assistant may do on a Project is the
  delegate rule: `delegate_permission_held(...)`, or
  `run_permission_held(...)` for one Run from its own row. Enqueue, the
  claim, the Run-proof reads, the voice reads and the Huddle work reads ask
  it; a new Run check asks it too rather than restating it. Two file checks
  keep their own predicate, because a file can be held by several Projects
  (`AuthorizeRunAttachmentDownload`, `ListRunChannelAttachments`), and
  `execution.TestDelegateRuleUsesTheAuthorizersAccess` holds them to the
  rule. Neither function is inlined: one call per candidate Project or
  claim candidate, never inside a list over a Company's Tasks. Bind the
  permission, read and the Company default set from `auth`.
- Company-agnostic internal queries stay narrow and bounded (LIMIT or keyset).
