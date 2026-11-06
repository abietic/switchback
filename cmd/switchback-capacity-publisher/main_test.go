package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCommandClosedArguments(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"publisher", "--enable"}, {"publisher", "--key=/tmp/key"}, {"publisher", "-"}} {
		calls := 0
		if run(context.Background(), args, func(context.Context) error { calls++; return nil }) != 2 || calls != 0 {
			t.Fatal("argument reached publisher", args, calls)
		}
	}
	if run(nil, []string{"publisher"}, func(context.Context) error { return nil }) != 2 || run(context.Background(), []string{"publisher"}, nil) != 2 {
		t.Fatal("invalid construction accepted")
	}
}
func TestCommandContextAndExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, fail := range []bool{false, true} {
		calls := 0
		want := 0
		if fail {
			want = 1
		}
		code := run(ctx, []string{"publisher"}, func(got context.Context) error {
			calls++
			if got != ctx {
				t.Fatal("context replaced")
			}
			if fail {
				return errors.New("secret path must not be printed")
			}
			return nil
		})
		if code != want || calls != 1 {
			t.Fatal(code, calls)
		}
	}
}

func TestCommandProductionBinding(t *testing.T) {
	f, e := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	bound := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if s, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok && id.Name == "os" {
				for _, bad := range []string{"Getenv", "LookupEnv", "ReadFile", "Open"} {
					if s.Sel.Name == bad {
						t.Fatal("command configuration IO", bad)
					}
				}
			}
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "run" && len(call.Args) == 3 {
			if s, ok := call.Args[2].(*ast.SelectorExpr); ok {
				if id, ok := s.X.(*ast.Ident); ok && id.Name == "capacitypublisher" && s.Sel.Name == "Run" {
					bound = true
				}
			}
		}
		return true
	})
	if !bound {
		t.Fatal("main does not bind fixed production Run")
	}
}
