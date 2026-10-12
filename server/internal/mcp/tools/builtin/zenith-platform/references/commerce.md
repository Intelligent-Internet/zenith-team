# Commerce from a Task Run

All commands accept `--output json`. Ids are UUIDs; the Task may also be
named by its key (`ENG-7`) or its displayed label where a Task id is accepted.

## Commerce (Task Runs)

```
zen commerce account-status
zen commerce manifest get <manifest-id>
zen commerce dev --manifest boardly-commerce.json -- npm run dev
```

Manifest submission stays on the `commerce_manifest_submit` MCP tool.
