package contentbundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// DecodeJSON accepts one JSON value, with exact field names and no duplicate keys.
// It preserves the strict manifest semantics shared by the release importer and loader.
func DecodeJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return fmt.Errorf("line %d: %w", bytes.Count(raw[:syntaxErr.Offset], []byte("\n"))+1, err)
		}
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("holds more than one JSON value")
	}
	fields := map[string]bool{}
	fieldPaths(reflect.TypeOf(value), "", fields)
	return checkFieldNames(json.NewDecoder(bytes.NewReader(raw)), "", fields)
}

// fieldPaths adds the json field paths of a struct type: each field's name
// and, below a struct-valued field, "name.inner". A slice adds no step.
func fieldPaths(kind reflect.Type, prefix string, paths map[string]bool) {
	for kind.Kind() == reflect.Pointer || kind.Kind() == reflect.Slice {
		kind = kind.Elem()
	}
	if kind.Kind() != reflect.Struct {
		return
	}
	for index := range kind.NumField() {
		field := kind.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		paths[prefix+name] = true
		fieldPaths(field.Type, prefix+name+".", paths)
	}
}

// checkFieldNames walks one JSON value that already decoded and refuses an
// object key that is not one of the field paths, exactly, or that repeats
// within its object.
func checkFieldNames(decoder *json.Decoder, prefix string, fields map[string]bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			// Inside an object, every other token is a key, and a key is a string.
			name := prefix + fmt.Sprint(key)
			switch {
			case seen[name]:
				return fmt.Errorf("json: field %q appears more than once", name)
			case !fields[name]:
				return fmt.Errorf("json: unknown field %q; field names are case-sensitive", name)
			}
			seen[name] = true
			if err := checkFieldNames(decoder, name+".", fields); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for decoder.More() {
			if err := checkFieldNames(decoder, prefix, fields); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	_, err = decoder.Token()
	return err
}
