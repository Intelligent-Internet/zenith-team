package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/go-playground/validator/v10"
)

// errExplicitNull is reported from UnmarshalJSON rather than from a field rule
// so it lands as 400 with the other shape failures, not as 422 with the
// semantic ones.
var errExplicitNull = errors.New("explicit JSON null is not allowed")

// optional is a request-body field that tells an absent key from a supplied
// one — a distinction a plain *T cannot make, because encoding/json leaves
// both as nil.
//
// It rejects an explicit null instead of reading it as a clear. On a sparse
// PATCH the two are different statements, and no field using this type is
// clearable, so accepting null could only either write a zero value nobody
// asked for or drop the key in silence.
//
// The zero value means absent, so the at-least-one rules can count supplied
// fields straight off the decoded struct.
//
// A value rule in a `binding` tag reads T, not the wrapper. The validator has
// no rule for a struct and would panic with "Bad field type", so every
// instantiation a request DTO uses is registered with optionalValue — see
// registerOptionalTypes — which unwraps the field before any rule sees it. So
// `binding:"revision"` on an optional[int64] means what it means on a plain
// int64, and the bound stops being the service's to restate.
//
// Presence is not a value rule and does not move: the wrapper is the only
// thing that knows whether the key was supplied, and bindJSONBody is the only
// thing that reads it. `binding:"required"` on a field of this type is that
// rule, and it is spelt `omitnil,required` so the two stay separate — see
// optionalValue for what the leading omitnil buys.
//
// Every other binding rule, including the conditional required_without family,
// runs here normally, on the unwrapped value.
type optional[T any] struct {
	set   bool
	value T
}

// UnmarshalJSON runs only when the key is present: encoding/json skips it for
// an absent key and calls it for an explicit null.
func (o *optional[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errExplicitNull
	}
	// DisallowUnknownFields on the outer decoder does not reach a value a
	// custom unmarshaler decodes, so it is re-armed here; without it a nested
	// object silently accepts fields the contract does not define.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&o.value); err != nil {
		return err
	}
	o.set = true
	return nil
}

// present reports whether the key was supplied.
func (o optional[T]) present() bool { return o.set }

// ptr renders the field the way domain update inputs take it: nil for an
// absent key, meaning leave the column as it is.
func (o optional[T]) ptr() *T {
	if !o.set {
		return nil
	}
	return &o.value
}

// optionalValue is the validator's view of an optional[T]. A type registered
// through RegisterCustomTypeFunc is replaced by whatever this returns before a
// single rule runs, which is what lets a `binding` tag on the wrapper mean
// what it means on the value: `revision` on an optional[int64] is the same
// rule as `revision` on a plain int64, rather than the "Bad field type" panic
// a rule on a struct produces.
//
// It returns *T, and that pointer is the whole of the absent-versus-zero
// question. A supplied key surfaces as a non-nil pointer, which the validator
// dereferences and marks nullable, so the rule reads exactly what the caller
// sent — zero included, which matters because a zero revision is the value the
// rule exists to reject. An absent key surfaces as a typed nil, which is not a
// value at all, and no value rule may be answered from it.
//
// Turning that nil into "skip the rules" is the leading `omitnil` on the tag,
// because the validator's own default for a nil field is to fail the first
// rule rather than to skip it. That is also why omitnil is not optional
// dressing: without it the validator answers an absent key itself — bare
// `required` fails on a nil pointer — and presence ends up owned twice, here
// and in bindJSONBody's walk. With it the validator declines the field
// outright and the walk stays the single enforcement, which is what its own
// doc claims and what TestBindRequestEnforcesRequiredOnOptionalFields pins.
func optionalValue[T any](field reflect.Value) any {
	wrapper, ok := field.Interface().(optional[T])
	if !ok {
		// RegisterCustomTypeFunc binds this function to exactly optional[T],
		// so another type reaching it means the registration below is wrong.
		panic(fmt.Sprintf(
			"handler: optionalValue[%T] applied to a %s field", wrapper.value, field.Type(),
		))
	}
	return wrapper.ptr()
}

// requestOptionalTypes pairs every optional[T] the package's request bodies
// declare with the unwrapper for that T. Both halves are needed because a
// generic function cannot be selected from a reflect.Type at run time: the
// instantiation has to be written down.
//
// An instantiation missing from this list is not a compile error. Its fields
// keep the pre-registration behavior instead — a value rule on one panics at
// the first request that carries it, and `required` is answered from the
// wrapper's zero struct rather than from the key. TestEveryRequestOptionalIsRegistered
// reads the DTOs and pins the list against them.
var requestOptionalTypes = []struct {
	zero   any
	unwrap validator.CustomTypeFunc
}{
	{optional[bool]{}, optionalValue[bool]},
	{optional[int32]{}, optionalValue[int32]},
	{optional[int64]{}, optionalValue[int64]},
	{optional[string]{}, optionalValue[string]},
}

// registerOptionalTypes teaches the validator to read through the wrapper.
func registerOptionalTypes(engine *validator.Validate) {
	for _, entry := range requestOptionalTypes {
		engine.RegisterCustomTypeFunc(entry.unwrap, entry.zero)
	}
}
