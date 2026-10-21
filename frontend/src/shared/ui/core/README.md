# Zenith Team vendored core UI subset

This folder is a vendored copy of the small part of an internal core UI package
that Zenith Team uses. It is maintained here. See [NOTICE.md](NOTICE.md) for its
provenance and the licenses of the open-source parts.

Keep this folder scoped to components imported by product code and their local
dependencies. Do not copy the whole upstream package unless the app starts using those
exports.

All vendored UI icons must follow the [mandatory Iconsax instructions](../../../../AGENTS.md).
Replace upstream icon-library imports with `@/shared/ui/icons` or
`@/shared/ui/icon`; do not copy a component's other icon dependencies or SVG
artwork into this folder.
