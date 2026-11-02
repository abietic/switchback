package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

type fullStackEvidenceWriter func(output, name string, value any) error

// checkFullStackEvidence recomputes scoped evidence without writing claim files.
// Inputs are trusted local captures; this does not fence concurrent host mutation.
func checkFullStackEvidence(kind, project, output string, stdout io.Writer) error {
	if kind != "cold" && kind != "matrix" {
		return fmt.Errorf("unsupported full-stack evidence kind %q", kind)
	}
	if stdout == nil {
		return fmt.Errorf("full-stack evidence stdout is required")
	}
	if !projectPattern.MatchString(project) || !filepath.IsAbs(output) || filepath.Clean(output) != output {
		return fmt.Errorf("claim identity invalid")
	}
	canonical, err := filepath.EvalSymlinks(output)
	if err != nil || canonical != output {
		return fmt.Errorf("claim output must be canonical")
	}
	raw, err := readFullStackInput(output, "claim.json")
	if err != nil {
		return err
	}
	var claim map[string]string
	if json.Unmarshal(raw, &claim) != nil || claim["project"] != project || claim["output"] != output {
		return fmt.Errorf("claim mismatch")
	}
	save := func(_ string, _ string, value any) error {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		n, err := stdout.Write(raw)
		if err != nil {
			return err
		}
		if n != len(raw) {
			return io.ErrShortWrite
		}
		return nil
	}
	if kind == "cold" {
		return verifyFullStackColdWithWriter(project, output, save)
	}
	return verifyFullStackMatrixWithWriter(project, output, save)
}
