package capacitypublisher

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
)

func TestPublisherBootstrapPrecedesAllConstruction(t *testing.T) {
	for _, failure := range []string{"pending", "invalid", "cancel", "timeout", "success"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			q, err := bootstrapPublisher(context.Background(), func(context.Context, string, string) (*capacitysnapshot.DeploymentQualification, error) {
				calls++
				if failure != "success" {
					return nil, errors.New(failure)
				}
				return &capacitysnapshot.DeploymentQualification{}, nil
			})
			if failure == "success" {
				if err != nil || calls != 1 || q == nil {
					t.Fatalf("success %v %d", err, calls)
				}
			} else if err == nil || calls != 1 || q != nil {
				t.Fatalf("failure %v %d", err, calls)
			}
		})
	}
}

func TestPublisherBootstrapDominatesRealConstruction(t *testing.T) {
	tree, err := parser.ParseFile(token.NewFileSet(), "publish.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, d := range tree.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "Run" {
			run = f
		}
	}
	if run == nil {
		t.Fatal("Run missing")
	}
	gate := -1
	guard := -1
	construction := 0
	for i, s := range run.Body.List {
		ast.Inspect(s, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok {
					if id.Name == "bootstrapPublisher" {
						gate = i
					}
					for _, name := range []string{"newPublisherFiles", "readPublisherConfig", "verifyPublisherDeployment", "runPublisher"} {
						if id.Name == name {
							construction++
							if gate < 0 || guard < gate || i <= guard {
								t.Fatalf("%s before bootstrap guard", name)
							}
						}
					}
				}
			}
			return true
		})
		if branch, ok := s.(*ast.IfStmt); ok && gate >= 0 && i == gate+1 {
			if binary, ok := branch.Cond.(*ast.BinaryExpr); ok {
				if id, ok := binary.X.(*ast.Ident); ok && id.Name == "e" && binary.Op == token.NEQ {
					if len(branch.Body.List) == 1 {
						if _, ok := branch.Body.List[0].(*ast.ReturnStmt); ok {
							guard = i
						}
					}
				}
			}
		}
	}
	if gate < 0 || guard != gate+1 || construction < 4 {
		t.Fatal("bootstrap is not an unconditional pre-construction barrier")
	}
}
