package util

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-playground/validator/v10"
)

// BindJSON decodes one JSON object from r into a struct pointer, ignoring
// fields the destination does not declare.
//
// Reach for this only where tolerating an undeclared field is the point —
// reading a document an older or newer peer wrote. For anything a client
// sends, use BindStrictJSON: a misspelled field is a change the caller
// believes took effect.
func BindJSON(r io.Reader, destination any) error {
	return bindJSON(r, destination, true)
}

// BindStrictJSON decodes one JSON object from r into a struct pointer and
// refuses a field the destination does not declare.
func BindStrictJSON(r io.Reader, destination any) error {
	return bindJSON(r, destination, false)
}

// bindJSON reads r to the end and decodes one JSON object out of it.
//
// json.NewDecoder(r).Decode(&v) already refuses an array or scalar root and an
// empty body, and DisallowUnknownFields covers the fourth case. Three inputs it
// accepts in silence are the reason this is longer than one line — each returns
// a nil error and leaves the caller believing the body was read as sent:
//
//   - a bare null leaves the struct zero, so a write reads as "change nothing"
//     rather than "this is not a request";
//   - trailing data is dropped — `{"a":1} {"a":2}` decodes the first object and
//     discards the second, so sender and receiver disagree about what was sent;
//   - invalid UTF-8 is replaced with U+FFFD and stored, corrupting the value
//     instead of refusing it.
//
// The read error is returned unwrapped so a caller that capped r with
// http.MaxBytesReader can still match *http.MaxBytesError and answer 413.
// Everything else is plain encoding/json semantics.
func bindJSON(r io.Reader, destination any, allowUnknownFields bool) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(trimmed) {
		return errors.New("JSON body must be one object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if !allowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON body contains trailing data")
		}
		return err
	}
	return nil
}

// ConfigureValidator points a validator at JSON field names, so a failure is
// reported as `localPath` — what the caller sent — rather than `LocalPath`,
// and pins the `uuid` tag to the canonical lowercase form.
//
// It decorates an existing instance rather than constructing one because the
// instance it configures is gin's, binding.Validator.Engine(). There is one
// validator in the process and every `binding:"…"` tag is read by the same
// rules.
func ConfigureValidator(v *validator.Validate) {
	v.RegisterTagNameFunc(jsonFieldName)
	// The library's own `uuid` tag accepts uppercase since v10.30.5; the wire
	// contract is the lowercase form the server mints.
	if err := v.RegisterValidation("uuid", canonicalUUID); err != nil {
		panic(fmt.Sprintf("util: register uuid validation: %v", err))
	}
}

var canonicalUUIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// canonicalUUID reads a string field, or a value with a String method, as the
// library's tag did.
func canonicalUUID(fl validator.FieldLevel) bool {
	field := fl.Field()
	if field.Kind() == reflect.String {
		return canonicalUUIDPattern.MatchString(field.String())
	}
	if field.CanInterface() {
		if stringer, ok := field.Interface().(fmt.Stringer); ok {
			return canonicalUUIDPattern.MatchString(stringer.String())
		}
	}
	return false
}

func jsonFieldName(field reflect.StructField) string {
	name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
	if name == "-" {
		return ""
	}
	return name
}
