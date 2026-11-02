//go:build integration

package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/abietic/switchback/internal/testsupport/postgrestest"
)

// The driver intentionally uses only SELECTs. Prepare every actual SQL literal
// against the current migrated PostgreSQL schema, including evidence-map SQL,
// so a compilable driver cannot hide misspelled physical-only query columns.
func TestAcceptanceQueriesAgainstMigratedPostgres(t *testing.T) {
	if os.Getenv("SWITCHBACK_TEST_DATABASE_URL") == "" {
		t.Fatal("real PostgreSQL URL required; no skips")
	}
	pool := postgrestest.Open(t)
	ctx := context.Background()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	count := 0
	for _, path := range []string{"scenario.go", "fixture_control.go", "matrix.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			query, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			query = strings.TrimSpace(query)
			if !strings.HasPrefix(query, "SELECT ") && !strings.HasPrefix(query, "WITH ") {
				return true
			}
			count++
			if _, err = connection.Conn().Prepare(ctx, fmt.Sprintf("acceptance_%d", count), query); err != nil {
				t.Errorf("%s query %d: %v", path, count, err)
			}
			return true
		})
	}
	if count < 15 {
		t.Fatalf("query coverage unexpectedly low: %d", count)
	}
	t.Logf("prepared %d actual driver SELECT statements", count)
}
