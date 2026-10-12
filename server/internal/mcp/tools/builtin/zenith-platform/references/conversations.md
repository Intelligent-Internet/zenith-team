# The Conversation

All commands accept `--output json`. Ids are UUIDs; the Task may also be
named by its key (`ENG-7`) or its displayed label where a Task id is accepted.

## Conversation

```
zen conversation messages [--after-seq N] [--through-seq N] [--limit N]
zen conversation transcript [--after-seq N] [--limit N]
```

Messages are what people and the Agent said (`seq`, `type`, `author`,
`content`). The transcript is every durable provider item (tool calls,
outputs) across Runs. Page forward with `--after-seq` using the highest
`seq` you have seen. Scroll back with `--through-seq N`: it returns the
newest messages at or below `seq` N, so to read what came before the
earliest message you can see, pass that message's `seq` minus one.
