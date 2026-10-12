package handler

import (
	"bytes"
	"encoding/json"
	"time"

	"boardly.dev/boardly/server/internal/task"
)

func parseTaskLabels(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var labels []*string
	if err := json.Unmarshal(raw, &labels); err != nil || labels == nil {
		return nil, task.ErrInvalidInput
	}
	values := make([]string, len(labels))
	for index, label := range labels {
		if label == nil {
			return nil, task.ErrInvalidInput
		}
		values[index] = *label
	}
	return values, nil
}

func parseTaskDate(raw json.RawMessage) (task.DateChange, error) {
	if len(raw) == 0 {
		return task.DateChange{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return task.DateChange{Present: true}, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return task.DateChange{}, task.ErrInvalidInput
	}
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil || parsed.Year() < 1 || parsed.Format(time.DateOnly) != value {
		return task.DateChange{}, task.ErrInvalidInput
	}
	return task.DateChange{Present: true, Value: &value}, nil
}
