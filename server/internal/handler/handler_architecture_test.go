package handler

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHandlerArchitectureUsesOneFlatTransportHandler(t *testing.T) {
	t.Parallel()

	files := parseProductionHandlerFiles(t)
	handlerStructs := make([]string, 0)
	optionsStructs := make([]string, 0)
	handlerFields := make(map[string]struct{})
	misnamedServiceFields := make([]string, 0)
	freeRegistrars := make([]string, 0)
	domainHandlerLiterals := make([]string, 0)
	routeHandlerLiterals := make([]string, 0)

	for path, file := range files {
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.GenDecl:
				for _, specification := range declaration.Specs {
					typeSpecification, ok := specification.(*ast.TypeSpec)
					if !ok {
						continue
					}
					structType, ok := typeSpecification.Type.(*ast.StructType)
					if !ok {
						continue
					}
					if typeSpecification.Name.Name == "Options" {
						optionsStructs = append(optionsStructs, path+":Options")
					}
					if strings.HasSuffix(typeSpecification.Name.Name, "Handler") {
						handlerStructs = append(
							handlerStructs,
							path+":"+typeSpecification.Name.Name,
						)
					}
					if typeSpecification.Name.Name != "Handler" {
						continue
					}
					for _, field := range structType.Fields.List {
						for _, name := range field.Names {
							handlerFields[name.Name] = struct{}{}
						}
						serviceType, ok := field.Type.(*ast.Ident)
						if !ok || !strings.HasSuffix(serviceType.Name, "Service") {
							continue
						}
						for _, name := range field.Names {
							if name.Name != serviceType.Name {
								misnamedServiceFields = append(
									misnamedServiceFields,
									name.Name+":"+serviceType.Name,
								)
							}
						}
					}
				}
			case *ast.FuncDecl:
				if isRouteRegistrar(declaration.Name.Name) &&
					handlerReceiverName(declaration) != "Handler" {
					freeRegistrars = append(
						freeRegistrars,
						path+":"+declaration.Name.Name,
					)
				}
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CompositeLit:
				typeName, ok := node.Type.(*ast.Ident)
				if !ok || typeName.Name == "Handler" ||
					!strings.HasSuffix(typeName.Name, "Handler") {
					return true
				}
				domainHandlerLiterals = append(
					domainHandlerLiterals,
					path+":"+typeName.Name,
				)
			case *ast.CallExpr:
				selector, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || !isRouteMethod(selector.Sel.Name) {
					return true
				}
				for _, argument := range node.Args {
					ast.Inspect(argument, func(argumentNode ast.Node) bool {
						if _, ok := argumentNode.(*ast.FuncLit); ok {
							routeHandlerLiterals = append(
								routeHandlerLiterals,
								path+":"+selector.Sel.Name,
							)
							return false
						}
						return true
					})
				}
			default:
				return true
			}
			return true
		})
	}

	slices.Sort(handlerStructs)
	slices.Sort(optionsStructs)
	slices.Sort(misnamedServiceFields)
	slices.Sort(freeRegistrars)
	slices.Sort(domainHandlerLiterals)
	slices.Sort(routeHandlerLiterals)

	if len(handlerStructs) != 1 ||
		!strings.HasSuffix(handlerStructs[0], ":Handler") {
		t.Errorf("production handler structs = %v, want only Handler", handlerStructs)
	}
	if len(optionsStructs) != 0 {
		t.Errorf("production Options structs = %v, want dependencies directly on Handler", optionsStructs)
	}
	if _, ok := handlerFields["dependencies"]; ok {
		t.Error("Handler has nested dependencies field, want flat dependency fields")
	}
	if len(misnamedServiceFields) != 0 {
		t.Errorf(
			"Handler service fields = %v, want field names to match service interface names",
			misnamedServiceFields,
		)
	}
	for _, field := range []string{
		"AccountService",
		"AuthService",
		"GoogleOAuthService",
		"TaskService",
		"router",
	} {
		if _, ok := handlerFields[field]; !ok {
			t.Errorf("Handler is missing flat field %q", field)
		}
	}
	if len(freeRegistrars) != 0 {
		t.Errorf("route registrars without *Handler receiver = %v", freeRegistrars)
	}
	if len(domainHandlerLiterals) != 0 {
		t.Errorf("domain handler composite literals = %v, want none", domainHandlerLiterals)
	}
	if len(routeHandlerLiterals) != 0 {
		t.Errorf("inline Gin route handler literals = %v, want named Handler methods", routeHandlerLiterals)
	}
}

func parseProductionHandlerFiles(t *testing.T) map[string]*ast.File {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read handler directory: %v", err)
	}

	files := make(map[string]*ast.File)
	fileSet := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(
			fileSet,
			filepath.Clean(name),
			nil,
			parser.SkipObjectResolution,
		)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = file
	}
	// Four scanners share this helper. An empty map would turn all of them
	// green at once — the loudest possible failure reported as success.
	if len(files) == 0 {
		t.Fatal("parsed no production handler files; the scan has lost its directory")
	}
	return files
}

func isRouteRegistrar(name string) bool {
	return strings.HasPrefix(name, "register") &&
		(strings.HasSuffix(name, "Route") || strings.HasSuffix(name, "Routes"))
}

func isRouteMethod(name string) bool {
	return slices.Contains(
		[]string{"Any", "DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"},
		name,
	)
}

func handlerReceiverName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return ""
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	identifier, _ := receiver.(*ast.Ident)
	if identifier == nil {
		return ""
	}
	return identifier.Name
}
