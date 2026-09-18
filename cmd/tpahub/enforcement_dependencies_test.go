package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEnforcementDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./internal/enforcement")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dependency graph: %v %s", err, out)
	}
	for _, dependency := range strings.Fields(string(out)) {
		if strings.HasPrefix(dependency, "github.com/eugen252009/tpahub/") && dependency != "github.com/eugen252009/tpahub/internal/enforcement" {
			t.Errorf("contract/none depends on application authority: %s", dependency)
		}
	}
	// Concrete strict behavior and wiring share the combined executable package.
	// Inspect those source boundaries as well, rather than claiming the whole
	// executable's graph excludes its optional hosted composition.
	for _, path := range []string{"delivery_strict.go", "enforcement_composition.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"/internal/billing", "/internal/hostedbilling", "/internal/payments", "/internal/usage"} {
				if strings.Contains(name, forbidden) {
					t.Errorf("enforcement file %s imports %s", path, name)
				}
			}
			if path == "delivery_strict.go" && strings.Contains(name, "/internal/store") {
				t.Error("strict implementation bypassed its accounting port")
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, forbidden := range []string{"billing", "payment", "stripe", "currency", "ratecard", "invoice", "checkout"} {
				if strings.Contains(strings.ToLower(id.Name), forbidden) {
					t.Errorf("enforcement file %s references %s", path, id.Name)
				}
			}
			return true
		})
	}
}
