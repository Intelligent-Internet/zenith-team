# Inline component compatibility

Zenith implements the observable inline syntax in the supplied component
reference. It does not implement or claim access to ChatGPT's private extraction
protocol. See SKILL.md for the supported authoring contract.

Chat and Pages share the same scanner and JavaScript AST parser. Author code runs
only in a bounded interpreter, never eval or browser Function. Fences are literal.
UI actions require the current host's authority and a user interaction.

Zenith draws Iconsax Outline glyphs by their own names. The reference's Lucide
names are deliberately not translated; unknown names are diagnosed and
omitted, never replaced with another glyph. Media
requires resolved sources. Grounded citations, retrieval, maps and ecosystem
widgets require a connected Zenith capability; missing capabilities are explicit.
An unsupported state is not a fabricated source, search result or successful action.
