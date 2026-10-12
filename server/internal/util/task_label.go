package util

import (
	"strconv"
	"strings"
)

// ParseTaskLabel splits the label every surface shows for a Task,
// {taskPrefix}-{number} as in ENG-12 (protocol.TaskLabel), into its prefix
// and positive number. Anything else is false: a plain number, a slug, a
// second dash, or a number with a sign or any character but a digit. The
// server's name lookup and the zen CLI both read labels here, so one name
// never means one Task to the CLI and another to the server.
func ParseTaskLabel(reference string) (string, int64, bool) {
	prefix, digits, found := strings.Cut(reference, "-")
	if !found || prefix == "" || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return "", 0, false
	}
	number, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || number <= 0 {
		return "", 0, false
	}
	return prefix, number, true
}
