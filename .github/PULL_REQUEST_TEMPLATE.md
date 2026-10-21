## What and why

<!-- What does this change do, and why is it needed? Keep it to one topic. -->

## Type of change

<!-- Check one. A maintainer adds the matching label for the release notes. -->

- [ ] Bug fix (`bug`)
- [ ] New feature (`enhancement`)
- [ ] Breaking change: existing setups or clients must change (`breaking`)
- [ ] Security fix (`security`)
- [ ] Documentation only (`documentation`)
- [ ] Other: refactor, tests, build or CI

## Related issue

<!-- Features need an agreed issue first. Bug and doc fixes can skip this. -->

Closes #

## How I tested

<!-- The commands you ran and their output, for example `make go-unit`. -->

```text

```

## Risk and rollback

<!-- What could break, and how to undo it. Say whether this adds a migration and whether it is safe to roll back past it. -->

## Screenshots

<!-- Required for UI changes: before and after, at a wide and a narrow window size. Delete this section otherwise. -->

## Checklist

- [ ] Tests added or updated (server changes)
- [ ] Docs updated, if behavior or setup changed
- [ ] Migrations are append-only: no merged migration was edited
- [ ] No secrets, tokens or internal URLs; test fixtures use clearly fake values
- [ ] `make api-generate` run and committed, if the API changed
- [ ] Every commit is signed off (`git commit -s`)

## AI assistance

<!-- Optional. If you used an AI tool, name it here. You must understand every line you submit. -->
