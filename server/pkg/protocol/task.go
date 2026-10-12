package protocol

import "strconv"

// TaskLabel is the one string every surface displays for a Task: the
// company's task prefix and the Task's number, as in ENG-14. It is the key
// the server mints for a Task created without one, the label the CLI
// resolves, and the name a notification quotes. A Company Plan Task keeps
// the slug the plan chose as its key, so a key never stands in for the label.
func TaskLabel(taskPrefix string, number int64) string {
	return taskPrefix + "-" + strconv.FormatInt(number, 10)
}
