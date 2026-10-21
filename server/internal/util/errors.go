package util

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"

	"github.com/go-playground/validator/v10"
)

// maxRevisionText renders the `revision` rule's upper bound from the constant
// that defines it, so the number keeps one spelling in the source.
var maxRevisionText = strconv.FormatInt(protocol.MaxJSONSafeInteger, 10)

// RenderValidationErrors flattens validator.ValidationErrors into a
// deterministic, human-readable summary such as
// "id must be a canonical UUID; name is required". Messages use JSON field
// names, follow struct-field declaration order, never include submitted
// values, and the result is truncated to limitBytes on a rune boundary.
func RenderValidationErrors(err error, limitBytes int) string {
	var validationErrors validator.ValidationErrors
	if !errors.As(err, &validationErrors) || len(validationErrors) == 0 {
		return "validation failed"
	}
	parts := make([]string, 0, len(validationErrors))
	for _, fieldError := range validationErrors {
		parts = append(parts, renderFieldError(fieldError))
	}
	return truncateText(strings.Join(parts, "; "), limitBytes)
}

func renderFieldError(fieldError validator.FieldError) string {
	field := fieldPath(fieldError)
	switch fieldError.ActualTag() {
	case "required":
		return field + " is required"
	case "uuid":
		return field + " must be a canonical UUID"
	case "oneof":
		return field + " must be one of " + strings.Join(strings.Fields(fieldError.Param()), ", ")
	case "eq":
		return field + " must equal " + fieldError.Param()
	case "min":
		return field + " must be at least " + fieldError.Param() + sizeUnit(fieldError)
	case "max":
		return field + " must be at most " + fieldError.Param() + sizeUnit(fieldError)
	case "revision":
		return field + " must be a revision between 1 and " + maxRevisionText
	case "required_without_all":
		return field + " is required when none of its alternatives are provided"
	case "required_without":
		return field + " is required when its alternative is absent"
	case "excluded_with":
		return field + " must not be provided together with its alternative"
	default:
		return field + " is invalid"
	}
}

// fieldPath strips the root struct name from the error namespace so nested
// failures render as "ceo.id" or "runtimeIds[3]" rather than
// "createCompanyRequest.ceo.id".
func fieldPath(fieldError validator.FieldError) string {
	namespace := fieldError.Namespace()
	if index := strings.IndexByte(namespace, '.'); index >= 0 {
		return namespace[index+1:]
	}
	return fieldError.Field()
}

// sizeUnit disambiguates min/max messages: element counts for collections,
// character counts for strings, nothing for numbers.
func sizeUnit(fieldError validator.FieldError) string {
	switch fieldError.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return " items"
	case reflect.String:
		return " characters"
	default:
		return ""
	}
}

// truncateText caps value at limitBytes without splitting a UTF-8 rune,
// appending "..." when content was dropped. Non-positive limits return the
// value unchanged.
func truncateText(value string, limitBytes int) string {
	if limitBytes <= 0 || len(value) <= limitBytes {
		return value
	}
	const ellipsis = "..."
	if limitBytes <= len(ellipsis) {
		return ellipsis[:limitBytes]
	}
	cut := limitBytes - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + ellipsis
}
