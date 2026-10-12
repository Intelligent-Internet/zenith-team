package schedule

// Helpers shared by this package's unit tests and its `//go:build integration`
// suites. They live in an untagged file on purpose: declared inside a tagged
// file, the untagged build of this package would fail with `undefined:`.

const scheduleTestDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
