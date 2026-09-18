package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eugen252009/tpahub/internal/config"
	"github.com/eugen252009/tpahub/internal/provisioning"
)

func TestLocalProvisioningDependencyBoundary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./internal/provisioning", "./internal/tpahubcapability")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dependency graph: %v %s", err, out)
	}
	allowed := map[string]bool{"commerce": true, "provisioning": true, "tpahubcapability": true}
	prefix := "github.com/eugen252009/tpahub/internal/"
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, prefix) && !allowed[strings.TrimPrefix(dep, prefix)] {
			t.Errorf("local source depends on %s", dep)
		}
	}
	// Exercise the production selector: this must return the independent policy,
	// never the legacy Nano adapter, even when purchases are enabled.
	for _, purchases := range []bool{false, true} {
		source, err := initialGrantSource(config.Config{CapabilitySource: config.CapabilitiesLocal, BillingPurchasesEnabled: purchases})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := source.(provisioning.LocalPolicy); !ok {
			t.Fatalf("local composition returned %T", source)
		}
	}
	source, err := initialGrantSource(config.Config{CapabilitySource: config.CapabilitiesHosted, LocalProvisioningEnabled: true})
	if err != nil || source != nil {
		t.Fatalf("hosted registration fabricated a source: %T %v", source, err)
	}
	// The local branch's only callee remains the local configuration resolver.
	// The combined executable legitimately retains a legacy product adapter.
	f, err := parser.ParseFile(token.NewFileSet(), "provisioning.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		c, ok := n.(*ast.CaseClause)
		if !ok || len(c.List) != 1 {
			return true
		}
		sel, ok := c.List[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "CapabilitiesLocal" {
			return true
		}
		found = true
		var calls []string
		ast.Inspect(c, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if s, ok := call.Fun.(*ast.SelectorExpr); ok {
					calls = append(calls, s.Sel.Name)
				} else {
					calls = append(calls, "unexpected direct call")
				}
			}
			return true
		})
		if !reflect.DeepEqual(calls, []string{"ResolveLocalPolicy"}) {
			t.Errorf("unexpected local composition calls: %v", calls)
		}
		return false
	})
	if !found {
		t.Fatal("local composition branch missing")
	}
}
