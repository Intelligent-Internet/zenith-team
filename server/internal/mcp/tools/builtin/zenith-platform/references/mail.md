# Agent Mail drafts

All commands accept `--output json`. Ids are UUIDs; the Task may also be
named by its key (`ENG-7`) or its displayed label where a Task id is accepted.

## Mail

```
zen mail draft --to a@example.com --subject "..." --text-file ./body.txt
```

Creates reviewable Agent Mail drafts in this Conversation. Nothing is sent
until a member with `mailbox.manage` reviews and sends them. Available when
the Agent has Mail enabled and the Run acts for a member with that permission.
The Company permission catalog currently grants it only to owners. Enabling
Mail on the Agent does not grant the acting member any additional permission.

After creation, the draft opens in the email review panel on the right of this
chat. The member can review recipients and content, edit, and press Send there.
The email card in the chat reopens that panel. Do not direct the member to the
Company Email inbox to find outbound drafts, and never claim an email was sent
when this command only created a draft.
