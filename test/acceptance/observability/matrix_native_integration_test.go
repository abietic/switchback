//go:build integration && observability_native

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactlocal "github.com/abietic/switchback/internal/artifacttrace/adapter/localfile"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The retained failed claim is a read-only fixture for the actual acceptance
// verifier. It is deliberately opt-in so ordinary integration runs need no
// physical claim, and this test never starts a smoke or matrix scenario.
func TestSupplementProofsReadPublishedClaimThroughProductionLoader(t *testing.T) {
	output := os.Getenv("OBSERVABILITY_PROOF_TEST_OUTPUT")
	databaseURL := os.Getenv("OBSERVABILITY_PROOF_TEST_DATABASE_URL")
	if output == "" || databaseURL == "" {
		t.Skip("retained published claim and exact database URL required")
	}
	claimBytes, err := os.ReadFile(filepath.Join(output, "claim.json"))
	if err != nil {
		t.Fatal(err)
	}
	var claim map[string]string
	if err := json.Unmarshal(claimBytes, &claim); err != nil {
		t.Fatal(err)
	}
	if _, err := readClaim(claim["project"], output); err != nil {
		t.Fatal(err)
	}
	claimEnv, err := os.ReadFile(filepath.Join(output, "acceptance.env"))
	if err != nil {
		t.Fatal(err)
	}
	claimedPort := ""
	backendPort := ""
	for _, line := range strings.Split(string(claimEnv), "\n") {
		if strings.HasPrefix(line, "OBSERVABILITY_POSTGRES_PORT=") {
			claimedPort = strings.TrimPrefix(line, "OBSERVABILITY_POSTGRES_PORT=")
		}
		if strings.HasPrefix(line, "SWITCHBACK_BACKEND_PORT=") {
			backendPort = strings.TrimPrefix(line, "SWITCHBACK_BACKEND_PORT=")
		}
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if !stringsSafeDatabase(cfg.ConnConfig.Database, claim["project"]) || cfg.ConnConfig.Host != "127.0.0.1" || claimedPort == "" || fmt.Sprint(cfg.ConnConfig.Port) != claimedPort {
		t.Fatal("proof test database does not match retained loopback claim")
	}
	proofPool, err := openFragmentProofPool(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer proofPool.Close()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ctx := context.Background()
	metadataPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer metadataPool.Close()
	var metadataMode, proofMode string
	if err := metadataPool.QueryRow(ctx, `SHOW default_transaction_read_only`).Scan(&metadataMode); err != nil {
		t.Fatal(err)
	}
	if err := proofPool.QueryRow(ctx, `SHOW default_transaction_read_only`).Scan(&proofMode); err != nil {
		t.Fatal(err)
	}
	if metadataMode != "on" || proofMode != "off" {
		t.Fatalf("unexpected proof session modes: metadata=%s proof=%s", metadataMode, proofMode)
	}
	objects, err := artifactlocal.NewObjectStore(filepath.Join(output, "data"))
	if err != nil {
		t.Fatal(err)
	}
	nonce := strings.TrimPrefix(claim["project"], "switchback-obsv-")
	released, err := os.ReadFile(filepath.Join(output, "released-"+nonce+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected []ExpectedModel
	if err := json.Unmarshal(released, &expected); err != nil {
		t.Fatal(err)
	}
	if len(expected) != 2 || expected[0].TurnID == expected[1].TurnID {
		t.Fatal("retained claim must contain two released Turns")
	}
	turns := []string{expected[0].TurnID, expected[1].TurnID}
	proofs, ready, err := readSupplementProofs(ctx, metadataPool, proofPool, objects, turns, expected, false)
	if err != nil {
		t.Fatalf("production proof loader rejected six published sources: %v", err)
	}
	if !ready || len(proofs) != 6 {
		t.Fatalf("exact six-source proof incomplete: ready=%t proofs=%d", ready, len(proofs))
	}
	if backendPort == "" {
		t.Fatal("claim Backend port missing")
	}
	t.Setenv("OBSERVABILITY_DATABASE_URL", databaseURL)
	driver, err := openMatrixDriver(ctx, claim["project"], output, "http://127.0.0.1:"+backendPort)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.pool.Close()
	defer driver.proofPool.Close()
	proofs, ready, err = readSupplementProofs(ctx, driver.pool, driver.proofPool, driver.objects, turns, expected, false)
	if err != nil || !ready || len(proofs) != 6 {
		t.Fatalf("matrix proof path rejected exact six sources: ready=%t proofs=%d err=%v", ready, len(proofs), err)
	}
}
