package util

import "testing"

// A Task is named by people and Agents as its {prefix}-{number} label. The
// server's name lookup and the CLI's must agree on what a label is, so both
// ask this one parser.
func TestParseTaskLabelAcceptsOnlyAPrefixDashAndPositiveDigits(t *testing.T) {
	t.Parallel()

	for reference, want := range map[string]struct {
		prefix string
		number int64
		ok     bool
	}{
		"BSC-8":                    {"BSC", 8, true},
		"bsc-12":                   {"bsc", 12, true},
		"ENG-007":                  {"ENG", 7, true},
		"ship_release":             {},
		"7":                        {},
		"BSC-":                     {},
		"-8":                       {},
		"BSC-0":                    {},
		"BSC-8-9":                  {},
		"BSC-x8":                   {},
		"BSC-+8":                   {},
		"BSC--8":                   {},
		"BSC-8 ":                   {}, //nolint:gocritic // the trailing space is the case under test
		"BSC-99999999999999999999": {},
	} {
		prefix, number, ok := ParseTaskLabel(reference)
		if prefix != want.prefix || number != want.number || ok != want.ok {
			t.Errorf("ParseTaskLabel(%q) = %q, %d, %v; want %+v", reference, prefix, number, ok, want)
		}
	}
}
