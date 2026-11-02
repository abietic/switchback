// Command observability is a test-only HTTP/SQL evidence driver. It never owns a control hub.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

var projectPattern = regexp.MustCompile(`^switchback-obsv-[a-z0-9]{8,24}$`)

func claim(project, root string) error {
	if !projectPattern.MatchString(project) {
		return errors.New("project must be switchback-obsv- plus 8-24 unique lowercase alphanumerics")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return errors.New("output must be an exact absolute new directory")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return err
	}
	if parent != filepath.Dir(root) {
		return errors.New("output parent must be canonical without symlinks")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		return fmt.Errorf("exclusive output claim: %w", err)
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	value := map[string]string{"project": project, "output": root, "token": hex.EncodeToString(token), "boundary": "controlled ACP/model/Tool; not real Codex, not G5, not full logging/metrics G4"}
	encoded, _ := json.MarshalIndent(value, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "claim.json"), encoded, 0600); err != nil {
		return err
	}
	return freshEnvironment(project, root)
}

func main() {
	mode := flag.String("mode", "", "claim, capture, collect-intervals, collect-full-stack-cold, verify-full-stack-cold, check-full-stack-cold, smoke, snapshot, correlation-snapshot, correlate, run-matrix, run-full-stack-matrix, verify-full-stack-matrix, check-full-stack-matrix, fault-proxy, produce-report, validate")
	project := flag.String("project", "", "unique project")
	output := flag.String("output", "", "new exact output directory for claim; claimed directory otherwise")
	backend := flag.String("backend", "", "loopback Backend HTTP URL")
	report := flag.String("report", "", "report JSON to validate")
	listen := flag.String("listen", "127.0.0.1:18081", "fault-proxy listen address; root explicitly opts into container 0.0.0.0")
	block := flag.String("block", "", "ingestion or query")
	duration := flag.Duration("duration", 5*time.Minute, "bounded interval collection duration")
	flag.Parse()
	var err error
	switch *mode {
	case "claim":
		err = claim(*project, *output)
	case "validate":
		var data []byte
		data, err = os.ReadFile(*report)
		if err == nil {
			var r Report
			err = json.Unmarshal(data, &r)
			if err == nil {
				err = r.Validate()
			}
		}
	case "produce-report":
		err = routeProduceReport(*report, *output)
	case "collect-intervals":
		if *duration <= 0 || *duration > time.Hour {
			err = errors.New("collection duration must be within 0..1h")
			break
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		bounded, cancel := context.WithTimeout(ctx, *duration)
		err = collectIntervals(bounded, *project, *output)
		cancel()
		stop()
	case "collect-full-stack-cold":
		if *duration <= 0 || *duration > time.Hour {
			err = errors.New("collection duration must be within 0..1h")
			break
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		bounded, cancel := context.WithTimeout(ctx, *duration)
		err = collectFullStackCold(bounded, *project, *output)
		cancel()
		stop()
	case "verify-full-stack-cold":
		if *backend != "" || *report != "" {
			err = errors.New("full-stack cold verifier reads exact claim-owned evidence only")
		} else {
			err = verifyFullStackCold(*project, *output)
		}
	case "check-full-stack-cold":
		if *backend != "" || *report != "" {
			err = errors.New("full-stack cold verifier reads exact claim-owned evidence only")
		} else {
			err = checkFullStackEvidence("cold", *project, *output, os.Stdout)
		}
	case "capture":
		err = captureProject(*project, *output)
	case "smoke", "snapshot", "correlation-snapshot":
		err = runScenario(*mode, *project, *output, *backend)
	case "run-matrix":
		err = runMatrix(*project, *output, *backend)
	case "run-full-stack-matrix":
		err = runFullStackMatrix(*project, *output, *backend)
	case "verify-full-stack-matrix":
		if *backend != "" || *report != "" {
			err = errors.New("full-stack matrix verifier reads exact claim-owned evidence only")
		} else {
			err = verifyFullStackMatrix(*project, *output)
		}
	case "check-full-stack-matrix":
		if *backend != "" || *report != "" {
			err = errors.New("full-stack matrix verifier reads exact claim-owned evidence only")
		} else {
			err = checkFullStackEvidence("matrix", *project, *output, os.Stdout)
		}
	case "correlate":
		if *backend != "" {
			err = errors.New("correlate derives origins from claim; do not supply backend")
		} else {
			err = runCorrelation(*project, *output)
		}
	case "fault-proxy":
		err = serveFaultProxy(*listen, *block)
	default:
		err = errors.New("explicit supported mode required")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Preserve the original -report recipe.json -output result.json invocation.
// The new claimed-directory form reads only its matrix-recipe.json and writes
// the explicit, new final-report child path; neither form overwrites evidence.
func routeProduceReport(report, output string) error {
	if info, err := os.Stat(output); err == nil && info.IsDir() {
		data, err := os.ReadFile(filepath.Join(output, "claim.json"))
		if err != nil {
			return err
		}
		var c map[string]string
		if err = json.Unmarshal(data, &c); err != nil {
			return err
		}
		if _, err = readClaim(c["project"], output); err != nil {
			return err
		}
		if filepath.Dir(report) != output || filepath.Base(report) == "claim.json" || filepath.Base(report) == "matrix-recipe.json" {
			return errors.New("report must be a new file inside exact claim")
		}
		raw, e := os.ReadFile(filepath.Join(output, "matrix-recipe.json"))
		if e != nil {
			return e
		}
		var recipe ReportRecipe
		if e = json.Unmarshal(raw, &recipe); e != nil {
			return e
		}
		if e = validateRecipePaths(output, recipe); e != nil {
			return e
		}
		return produceReport(filepath.Join(output, "matrix-recipe.json"), report)
	}
	return produceReport(report, output)
}

func validateRecipePaths(root string, r ReportRecipe) error {
	paths := []string{r.Physical}
	for _, pair := range r.Pairs {
		paths = append(paths, pair.Healthy.Original, pair.Healthy.Recovered, pair.Outage.Original, pair.Outage.Recovered)
	}
	if r.ColdStart != nil {
		paths = append(paths, r.ColdStart.Before, r.ColdStart.After, r.ColdStart.Smoke)
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path || filepath.Dir(path) != root {
			return errors.New("evidence path is not an exact claim-owned file")
		}
	}
	return nil
}
