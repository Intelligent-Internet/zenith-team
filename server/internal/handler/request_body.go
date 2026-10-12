package handler

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/util"
	"boardly.dev/boardly/server/pkg/protocol"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// maxValidationDetailBytes caps the aggregated per-field message so the 422
// detail stays a bounded, single-line summary.
const maxValidationDetailBytes = 1024

// Gin's validator is the only one in the process, which is what makes a
// `binding:"…"` tag the entire declaration of a rule — including the
// cross-field ones, which are `required_without_all` and `excluded_with`
// rather than hand-written struct-level callbacks. It needs teaching three
// things: to report failures under the JSON field name the caller sent, how to
// read through the optional[T] wrapper so a tag on one of those fields is a
// rule about the value rather than a panic about the struct, and the rules
// this package names for itself, `revision` and `maxConcurrentRuns`.
func init() {
	engine, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		panic(
			fmt.Sprintf(
				"handler: gin validator engine is %T, want *validator.Validate",
				binding.Validator.Engine(),
			),
		)
	}
	util.ConfigureValidator(engine)
	registerOptionalTypes(engine)
	if err := engine.RegisterValidation(revisionRule, validRevision); err != nil {
		panic(fmt.Sprintf("handler: register %q validation: %v", revisionRule, err))
	}
	if err := engine.RegisterValidation(maxConcurrentRunsRule, validMaxConcurrentRuns); err != nil {
		panic(fmt.Sprintf("handler: register %q validation: %v", maxConcurrentRunsRule, err))
	}
}

// revisionRule names the bound every optimistic-concurrency field carries: a
// revision is a positive integer a JSON client round-trips exactly.
//
// It exists because a struct tag cannot interpolate a constant. Spelt inline
// the rule reads `min=1,max=9007199254740991`, which put a copy of
// protocol.MaxJSONSafeInteger on every plain ExpectedRevision field in the
// package and left the constant it came from unreferenced — so nothing in the
// source connected the number to its meaning.
const revisionRule = "revision"

// validRevision is the whole of that rule, read off the constant rather than
// off a literal in a tag.
//
// A field of any other kind panics, which is what the validator itself does
// for a numeric rule on a string: a rule applied to a type it cannot mean is
// a mistake in this package, and answering 422 would blame the caller for it.
// TestRevisionFieldsShareOneBinding pins which fields carry the tag, so the
// mistake surfaces in the test run rather than in a request.
func validRevision(field validator.FieldLevel) bool {
	value := field.Field()
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		revision := value.Int()
		return revision >= 1 && revision <= protocol.MaxJSONSafeInteger
	default:
		panic(fmt.Sprintf(
			"handler: binding:%q on a %s field, want a signed integer", revisionRule, value.Kind(),
		))
	}
}

// maxConcurrentRunsRule names the bound on an agent's task-lane run cap. Like
// `revision` it is a named rule rather than an inline `min=1,max=10`, so the
// bounds keep one spelling and the binder stays the only thing that refuses a
// value out of them: there is deliberately no DB CHECK and no service guard
// restating the rule.
//
// The two numbers themselves come from execution, next to the claim gate that
// acts on them, so the boundary that rejects a bad cap and the gate that has
// to survive one are reading the same pair. The gate's own handling of a value
// that never came through here — a row edited by hand — is to clamp and warn,
// which is a different job from refusing a request.
const maxConcurrentRunsRule = "maxConcurrentRuns"

// validMaxConcurrentRuns is the whole of that rule. As with validRevision, a
// field of any non-integer kind is a mistake in this package and panics rather
// than answering 422.
func validMaxConcurrentRuns(field validator.FieldLevel) bool {
	value := field.Field()
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		runs := value.Int()
		return runs >= execution.MinTaskLaneCapacity && runs <= execution.MaxTaskLaneCapacity
	default:
		panic(fmt.Sprintf(
			"handler: binding:%q on a %s field, want a signed integer",
			maxConcurrentRunsRule, value.Kind(),
		))
	}
}

// requestError says why a request body could not become a value, and carries
// the response that failure deserves. bindRequest returns it instead of
// writing a response itself, so a handler reads as ordinary Go — a value and
// an error — and one function owns the mapping to a status.
type requestError struct {
	key    boundaryErrorKey
	detail string
	cause  error
}

func (e *requestError) Error() string {
	detail := e.detail
	if detail == "" {
		detail = boundaryErrorResponseFor(e.key).detail
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", detail, e.cause)
	}
	return detail
}

func (e *requestError) Unwrap() error { return e.cause }

// bindRequest reads a JSON body into a T and applies its `binding` tags. It is
// the only way a handler should take a request body.
//
// Decoding is util.BindStrictJSON, which consumes exactly one UTF-8 object and
// rejects undeclared fields. Validation then uses gin's shared go-playground
// instance, so a tag is the entire declaration of a field rule — there is no
// second pass a call site could skip.
//
// Two things gin does not do, and each is contract rather than taste:
//
//   - The media type must be exactly application/json, or 415. ShouldBindJSON
//     binds whatever it is handed, so a form posted to a JSON route would
//     otherwise decode as an empty object and read as a valid empty request.
//   - The body is capped, or 413. Without the cap a single request can make
//     the server read until it runs out of memory.
//
// It returns the value and an error rather than writing a response and
// reporting a bool, so a handler reads as ordinary Go.
func bindRequest[T any](c *gin.Context, maxBytes int64) (T, error) {
	var value T

	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return value, &requestError{
			key: boundaryErrorJSONContentTypeRequired,
		}
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	if err := bindJSONBody(c, &value); err != nil {
		// One call produced both kinds of failure, so they are separated here:
		// a field rule that did not hold is 422, and anything that stopped the
		// bytes becoming a value at all is 400 or 413.
		var validationErrors validator.ValidationErrors
		if errors.As(err, &validationErrors) {
			return value, validationErrors
		}
		var unwalkable *validator.InvalidValidationError
		if errors.As(err, &unwalkable) {
			return value, err
		}
		// A failure that already decided its own status — the required-key
		// rule below — says so, and must not be re-read as a decode failure.
		var decided *requestError
		if errors.As(err, &decided) {
			return value, decided
		}
		return value, decodeFailure(err)
	}
	return value, nil
}

// presenceField is satisfied by optional[T] for every T: the wrapper is the
// only thing that knows whether its key was supplied.
type presenceField interface{ present() bool }

// bindJSONBody runs the strict decoder, gin's shared validator, and then the
// one rule the validator is not asked to answer.
//
// `binding:"required"` on an optional[T] is a statement about the key rather
// than about the value, and this walk is what enforces it.
//
// The validator could now be made to instead — optionalValue hands it a typed
// nil for an absent key, which its `required` rule already fails. But it only
// reaches a field it descends to, and it descends into a struct inside a slice
// only when the slice's own tag says `dive`. Presence would then hold or not
// depending on a tag one level up, which is the silent gap this walk exists to
// close. So the value rules are the validator's, presence is this walk's, and
// the `omitnil` every such field leads with is what keeps the validator out of
// the second question.
//
// The tag means exactly what it says. A missing key is a field rule that did
// not hold, which is 422 like every other one; an explicit null is refused
// earlier by the decoder as 400, because a null is a malformed statement about
// the field rather than an absent one.
func bindJSONBody[T any](c *gin.Context, value *T) error {
	if err := util.BindStrictJSON(c.Request.Body, value); err != nil {
		return err
	}
	if err := binding.Validator.ValidateStruct(value); err != nil {
		return err
	}
	return requireSuppliedOptionals(reflect.ValueOf(value).Elem(), "")
}

// requireSuppliedOptionals reports the first optional[T] field that declares
// `required` and was not supplied. It descends through the slices and nested
// structs a request body is built from, so a field inside `items[2]` is named
// as such rather than reported against the request as a whole.
func requireSuppliedOptionals(value reflect.Value, path string) error {
	// Pruning by type first is what keeps this proportional to the shape of
	// the request rather than to its size: several request types carry a
	// json.RawMessage, which is a []byte, and walking one means visiting every
	// byte of the body to learn what its type already said.
	if !mayContainOptional(value.Type(), make(map[reflect.Type]bool)) {
		return nil
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return nil
		}
		return requireSuppliedOptionals(value.Elem(), path)
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			if err := requireSuppliedOptionals(
				value.Index(index), fmt.Sprintf("%s[%d]", path, index),
			); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		return requireSuppliedStructFields(value, path)
	default:
		return nil
	}
}

func requireSuppliedStructFields(value reflect.Value, path string) error {
	structType := value.Type()
	for index := range structType.NumField() {
		field := structType.Field(index)
		if !field.IsExported() {
			continue
		}
		name := jsonFieldName(field)
		if path != "" {
			name = path + "." + name
		}

		supplied, isOptional := value.Field(index).Interface().(presenceField)
		if !isOptional {
			if err := requireSuppliedOptionals(value.Field(index), name); err != nil {
				return err
			}
			continue
		}
		if declaresRequired(field.Tag.Get("binding")) && !supplied.present() {
			return &requestError{
				key:    boundaryErrorValidationFailed,
				detail: name + " is required",
			}
		}
	}
	return nil
}

// optionalInterface is the presenceField interface as a reflect.Type, so a
// type can be asked whether it satisfies it without a value in hand.
var optionalInterface = reflect.TypeOf((*presenceField)(nil)).Elem()

// mayContainOptional reports whether a type could hold a presenceField
// anywhere inside it. It answers from the type alone, so the walk can skip a
// whole subtree — a string, a number, a json.RawMessage — without visiting it.
//
// JSON values are acyclic, but their Go types need not be. Each search owns
// its visited set so pruning a type never skips another value of that type.
func mayContainOptional(fieldType reflect.Type, seen map[reflect.Type]bool) bool {
	if fieldType.Implements(optionalInterface) {
		return true
	}
	if seen[fieldType] {
		return false
	}
	seen[fieldType] = true
	switch fieldType.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return mayContainOptional(fieldType.Elem(), seen)
	case reflect.Interface:
		// The concrete value is not known until it is walked.
		return true
	case reflect.Struct:
		for index := range fieldType.NumField() {
			if field := fieldType.Field(index); field.IsExported() &&
				mayContainOptional(field.Type, seen) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// declaresRequired reports whether a binding tag carries the bare `required`
// rule, which is the only rule the validator drops on these fields. The
// conditional forms are deliberately not handled here: the validator still
// applies them, and enforcing them twice would report the same failure from
// two places.
func declaresRequired(tag string) bool {
	for rule := range strings.SplitSeq(tag, ",") {
		switch strings.TrimSpace(rule) {
		case "dive":
			// Rules after dive apply to elements, not the collection field.
			return false
		case "required":
			return true
		}
	}
	return false
}

// jsonFieldName is the name the caller used, which is the only name a
// validation message may say back to them.
func jsonFieldName(field reflect.StructField) string {
	tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if tag == "" || tag == "-" {
		return field.Name
	}
	return tag
}

// decodeFailure separates the size cap from every other read failure, because
// they are the two different answers a caller can act on: send less, or send
// something else.
func decodeFailure(err error) *requestError {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &requestError{
			key:   boundaryErrorRequestBodyTooLarge,
			cause: err,
		}
	}
	return &requestError{
		key:   boundaryErrorInvalidRequestBody,
		cause: err,
	}
}

// writeRequestError sends the response a decode or validation failure calls
// for. Every request-shaped failure in the package funnels through here, so
// the status and code for each kind are decided once.
func writeRequestError(c *gin.Context, err error) {
	var failure *requestError
	if errors.As(err, &failure) {
		if failure.detail == "" {
			writeBoundaryError(c, failure.key)
		} else {
			writeBoundaryErrorDetail(c, failure.key, failure.detail)
		}
		return
	}
	// Everything else came from the validator. Its own error type renders per
	// field as 422; anything else is a programming error — it was handed
	// something it cannot walk — and no change the caller could make would
	// help, so it is a plain 500.
	var validationErrors validator.ValidationErrors
	if !errors.As(err, &validationErrors) {
		writeBoundaryError(c, boundaryErrorInternal)
		return
	}
	writeBoundaryErrorDetail(
		c,
		boundaryErrorValidationFailed,
		util.RenderValidationErrors(validationErrors, maxValidationDetailBytes),
	)
}
