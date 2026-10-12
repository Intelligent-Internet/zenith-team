package company

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCreateLocksEnabledFounderBeforeCompanyInsert(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}
	var calls []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Create" || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callee := call.Fun.(type) {
			case *ast.Ident:
				calls = append(calls, callee.Name)
			case *ast.SelectorExpr:
				calls = append(calls, callee.Sel.Name)
			}
			return true
		})
		break
	}
	userLock := createCallPosition(calls, "LockEnabledUserForMembershipMutation")
	companyInsert := createCallPosition(calls, "CreateCompany")
	if userLock < 0 || companyInsert < 0 || userLock >= companyInsert {
		t.Fatalf(
			"Create() call order = %v, want enabled User lock before Company insert",
			calls,
		)
	}
}

func createCallPosition(calls []string, name string) int {
	for index, call := range calls {
		if call == name {
			return index
		}
	}
	return -1
}
