package localproduct

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacityobserver"
)

func TestInitialCreatedLayerProductionCompositionUsesIPC(t *testing.T) {
	if got, want := reflect.TypeOf(initialCapacitySource{}.createdLayer), reflect.TypeOf((*capacityobserver.Checker)(nil)); got != want {
		t.Fatalf("production created-layer source = %v, want concrete %v", got, want)
	}
	// Pin the common constructor, including execution-disabled assembly, without
	// accessing a database, socket or key. Adapter tests exercise lazy IPC itself.
	f, err := parser.ParseFile(token.NewFileSet(), "runtime_capacity_source.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newInitialCapacitySource" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "assembleInitialCapacitySource" {
				calls = append(calls, id.Name)
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if owner, ok := sel.X.(*ast.Ident); ok {
					calls = append(calls, owner.Name+"."+sel.Sel.Name)
				}
			}
			return true
		})
	}
	var production []string
	for _, call := range calls {
		switch call {
		case "capacitysnapshot.ReadTrust", "assembleInitialCapacitySource", "capacitysnapshot.OpenDeploymentQualification", "workerpostgres.NewCapacityStore":
			production = append(production, call)
		case "capacitysnapshot.NewCreatedLayerReader", "os.Getenv", "os.LookupEnv":
			t.Fatalf("unexpected fallback or route switch: %s", call)
		}
	}
	want := []string{"capacitysnapshot.ReadTrust", "assembleInitialCapacitySource", "capacitysnapshot.OpenDeploymentQualification", "workerpostgres.NewCapacityStore"}
	if !reflect.DeepEqual(production, want) {
		t.Fatalf("production source assembly order = %v, want %v", production, want)
	}
}

// Constructor fields are function values; CallExpr-only inspection must not
// silently omit the concrete IPC observer and original snapshot reader.
func TestRuntimeCreatedLayerFactoryPinsConcreteReaderAndIPC(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "runtime_capacity_source.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newInitialCapacitySource" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			kv, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return true
			}
			sel, ok := kv.Value.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			owner, ok := sel.X.(*ast.Ident)
			if ok {
				pinned[key.Name] = owner.Name + "." + sel.Sel.Name
			}
			return true
		})
	}
	if pinned["reader"] != "capacitysnapshot.NewReader" || pinned["observer"] != "capacityobserver.New" {
		t.Fatalf("production factories not concrete: %v", pinned)
	}
}
