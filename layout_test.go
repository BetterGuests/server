package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func parsePackageFiles(t *testing.T) map[string]*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	files := make(map[string]*ast.File)
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = file
	}
	return files
}

func TestRoutesAreRegisteredOnlyInRoutesFile(t *testing.T) {
	registrars := map[string]bool{"Handle": true, "HandleFunc": true}

	for path, file := range parsePackageFiles(t) {
		if path == "routes.go" {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && registrars[selector.Sel.Name] {
				t.Errorf("%s registers a route with %s; routes belong in routes.go", path, selector.Sel.Name)
			}
			return true
		})
	}
}

func TestMainFileDeclaresOnlyMainAndRun(t *testing.T) {
	allowed := map[string]bool{"main": true, "run": true}

	for _, declaration := range parsePackageFiles(t)["main.go"].Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if !allowed[function.Name.Name] {
			t.Errorf("main.go declares %s; only main and run belong there", function.Name.Name)
		}
	}
}

func TestMainFileDoesNotImportDomainPackages(t *testing.T) {
	for _, spec := range parsePackageFiles(t)["main.go"].Imports {
		path := strings.Trim(spec.Path.Value, `"`)
		if strings.Contains(path, "/internal/") && !strings.HasSuffix(path, "/internal/logging") {
			t.Errorf("main.go imports %s; wiring belongs in wiring.go", path)
		}
	}
}
