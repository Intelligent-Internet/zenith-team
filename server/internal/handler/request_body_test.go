package handler

import (
	"bytes"
	"go/ast"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/canvas"
	"github.com/gin-gonic/gin"
)

func TestDeclaresRequiredCollectionBoundary(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, tag string
		want      bool
	}{
		{"optional collection", "omitempty,max=5,dive,required", false},
		{"element requirement", "max=5,dive,required", false},
		{"required collection", "required,min=1,dive,required", true},
		{"optional value presence", "omitnil,required", true},
		{"conditional requirement", "required_if=Kind edit", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := declaresRequired(test.tag); got != test.want {
				t.Fatalf("declaresRequired(%q) = %v, want %v", test.tag, got, test.want)
			}
		})
	}
}

// The recursive edge deliberately precedes the required field.
type recursiveOptionalRequest struct {
	Children []recursiveOptionalRequest `json:"children"`
	Next     *recursiveOptionalLink     `json:"next"`
	Revision optional[int64]            `json:"revision" binding:"omitnil,required"`
}

type recursiveOptionalLink struct {
	Parent *recursiveOptionalRequest `json:"parent"`
}

func bindRecursiveRequestBody[T any](body string) error {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	_, err := bindRequest[T](c, 1<<20)
	return err
}

func TestBindRequestRecursiveTypes(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		body string
		bind func(string) error
		want string
	}{
		"markdown page": {
			body: `{"requestKey":"11111111-1111-4111-8111-111111111111","title":"Page","markdown":"hello"}`,
			bind: bindRecursiveRequestBody[canvas.PageCreateInput],
		},
		"native page": {
			body: `{"requestKey":"11111111-1111-4111-8111-111111111111","title":"Page","content":{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"hello"}]}]}}`,
			bind: bindRecursiveRequestBody[canvas.PageCreateInput],
		},
		"theme edit without operations": {
			body: `{"requestKey":"11111111-1111-4111-8111-111111111111","summary":"Theme","theme":"compact"}`,
			bind: bindRecursiveRequestBody[canvas.PageEditInput],
		},
		"nil recursive fields": {
			body: `{"revision":0}`,
			bind: bindRecursiveRequestBody[recursiveOptionalRequest],
		},
		"empty recursive slice": {
			body: `{"revision":0,"children":[]}`,
			bind: bindRecursiveRequestBody[recursiveOptionalRequest],
		},
		"required after recursive field": {
			body: `{}`,
			bind: bindRecursiveRequestBody[recursiveOptionalRequest],
			want: "revision is required",
		},
		"second child still checked": {
			body: `{"revision":0,"children":[{"revision":1},{}]}`,
			bind: bindRecursiveRequestBody[recursiveOptionalRequest],
			want: "children[1].revision is required",
		},
		"mutual recursion": {
			body: `{"revision":0,"next":{"parent":{}}}`,
			bind: bindRecursiveRequestBody[recursiveOptionalRequest],
			want: "next.parent.revision is required",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := test.bind(test.body)
			if test.want == "" {
				if err != nil {
					t.Fatalf("bindRequest() error = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != test.want {
				t.Fatalf("bindRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

// requiredOptionalItem is the nested shape a request body reaches an optional
// through: a slice of structs, which the caller names by index.
type requiredOptionalItem struct {
	ID               string          `json:"id"`
	ExpectedRevision optional[int64] `json:"expectedRevision" binding:"omitnil,required"`
}

// Items carries no `dive`, deliberately: the validator descends into a slice
// only when a tag tells it to, so this is the shape where it cannot answer
// presence at all and the walk is the only thing that can.
type requiredOptionalRequest struct {
	Revision optional[int64]        `json:"revision" binding:"omitnil,required"`
	Note     optional[string]       `json:"note"`
	Items    []requiredOptionalItem `json:"items"`
}

// bindRequiredOptionalRequest runs one body through the real binder.
func bindRequiredOptionalRequest(t *testing.T, body string) (requiredOptionalRequest, error) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	return bindRequest[requiredOptionalRequest](c, 1<<20)
}

// TestBindRequestEnforcesRequiredOnOptionalFields covers the rule the
// validator is not asked to apply.
//
// `required` on an optional[T] is a statement about the key, and the leading
// `omitnil` keeps the validator from answering it from the value: it stops at
// the typed nil optionalValue hands back for an absent key. The walk answers
// it instead, and it reaches every shape a request body is built from rather
// than only the ones a `dive` tag opens. Every handler that dereferences such
// a field is relying on that, and the failure it produces when the rule does
// not hold is a nil-pointer panic served as 500.
func TestBindRequestEnforcesRequiredOnOptionalFields(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body       string
		wantStatus int
		wantDetail string
	}{
		"supplied": {
			body:       `{"revision":7}`,
			wantStatus: 0,
		},
		"supplied as zero is still supplied": {
			body:       `{"revision":0}`,
			wantStatus: 0,
		},
		"absent": {
			body:       `{"note":"hello"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantDetail: "revision is required",
		},
		"explicit null is a shape failure, not an absence": {
			body:       `{"revision":null}`,
			wantStatus: http.StatusBadRequest,
		},
		"absent inside a nested item is named by index": {
			body:       `{"revision":1,"items":[{"id":"a","expectedRevision":2},{"id":"b"}]}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantDetail: "items[1].expectedRevision is required",
		},
		"an unrequired optional may be absent": {
			body:       `{"revision":1}`,
			wantStatus: 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := bindRequiredOptionalRequest(t, test.body)
			if test.wantStatus == 0 {
				if err != nil {
					t.Fatalf("bindRequest(%s) error = %v, want none", test.body, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("bindRequest(%s) error = nil, want %d", test.body, test.wantStatus)
			}

			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			writeRequestError(c, err)
			if response.Code != test.wantStatus {
				t.Fatalf("bindRequest(%s) status = %d, want %d; body %s",
					test.body, response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantDetail != "" && !bytes.Contains(response.Body.Bytes(), []byte(test.wantDetail)) {
				t.Fatalf("bindRequest(%s) body = %s, want it to name %q",
					test.body, response.Body.String(), test.wantDetail)
			}
		})
	}
}

// TestNoRequestTypePutsAnOptionalWhereTheWalkCannotReach guards the shapes the
// rule above does not descend into.
//
// requireSuppliedOptionals walks structs and slices, which is every shape a
// request body is built from today. Two shapes it does not walk:
//
//   - a map value, because nothing in a request body is a map;
//   - the inside of another optional, because the wrapped value is unexported
//     and reflection cannot read it without unsafe.
//
// An optional declared in either place would carry `required`, be read as
// guarded, and be enforced by nothing — the same silent gap the walk exists to
// close. Neither is used today, so this keeps it that way rather than paying
// for a walk that handles them.
func TestNoRequestTypePutsAnOptionalWhereTheWalkCannotReach(t *testing.T) {
	t.Parallel()

	// optionalTypeArgument returns the type an `optional[…]` wraps.
	optionalTypeArgument := func(node ast.Expr) (ast.Expr, bool) {
		index, ok := node.(*ast.IndexExpr)
		if !ok {
			return nil, false
		}
		ident, ok := index.X.(*ast.Ident)
		if !ok || ident.Name != "optional" {
			return nil, false
		}
		return index.Index, true
	}

	files := parseProductionHandlerFiles(t)

	// Named types must be resolved, not just read: the shape that matters is
	// optional[someRequest] where someRequest's own fields carry the rule, and
	// the field's type expression alone says nothing about them.
	structs := make(map[string]*ast.StructType)
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if structType, isStruct := spec.Type.(*ast.StructType); isStruct {
				structs[spec.Name.Name] = structType
			}
			return true
		})
	}
	if len(structs) == 0 {
		t.Fatal("resolved no struct declarations; the scan cannot see the request types")
	}

	// declaresRequiredOptional reports whether a type — named or literal —
	// holds an optional field tagged `required`, following named types one
	// level at a time.
	var declaresRequiredOptional func(ast.Expr, int) bool
	declaresRequiredOptional = func(node ast.Expr, depth int) bool {
		if depth > 8 {
			return false
		}
		var structType *ast.StructType
		switch typed := node.(type) {
		case *ast.Ident:
			structType = structs[typed.Name]
		case *ast.StructType:
			structType = typed
		case *ast.StarExpr:
			return declaresRequiredOptional(typed.X, depth+1)
		case *ast.ArrayType:
			return declaresRequiredOptional(typed.Elt, depth+1)
		}
		if structType == nil {
			return false
		}
		for _, field := range structType.Fields.List {
			if _, isOptional := optionalTypeArgument(field.Type); isOptional {
				if field.Tag == nil {
					continue
				}
				tag, err := strconv.Unquote(field.Tag.Value)
				if err == nil && declaresRequired(reflect.StructTag(tag).Get("binding")) {
					return true
				}
				continue
			}
			if declaresRequiredOptional(field.Type, depth+1) {
				return true
			}
		}
		return false
	}

	for name, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			field, ok := node.(*ast.Field)
			if !ok {
				return true
			}

			unreachable, why := ast.Expr(nil), ""
			if mapType, isMap := field.Type.(*ast.MapType); isMap {
				unreachable, why = mapType.Value, "a map value"
			} else if wrapped, isOptional := optionalTypeArgument(field.Type); isOptional {
				unreachable, why = wrapped, "the inside of another optional"
			}
			if unreachable == nil {
				return true
			}
			if declaresRequiredOptional(unreachable, 0) {
				t.Errorf(
					"%s: a required optional sits in %s, which requireSuppliedOptionals does "+
						"not walk — the rule would enforce nothing",
					name, why,
				)
			}
			return true
		})
	}
}

// BenchmarkBindRequestWalksTheShapeNotTheBody pins the cost of the required-key
// walk against a body dominated by a json.RawMessage field.
//
// The walk is proportional to the number of fields, not the number of bytes: a
// RawMessage is a []byte, and descending into one would visit every byte of the
// body to rediscover what its element type already said. Measured on a 64 KiB
// body, dropping the type prune took this from 55 allocations to 196,451 and
// from 0.27 ms to 6.3 ms — so the prune is the difference between a rule that
// costs nothing and one that scales with the request.
func BenchmarkBindRequestWalksTheShapeNotTheBody(b *testing.B) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"expectedRevision":1,"name":"` + strings.Repeat("n", 64<<10) +
		`","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`)

	b.ReportAllocs()
	for b.Loop() {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPatch, "/", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		if _, err := bindRequest[updateCompanyProfileRequest](c, 1<<20); err != nil {
			b.Fatalf("bindRequest() error = %v", err)
		}
	}
}
