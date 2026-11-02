// Package failurematrix records executable acceptance results for production
// failure variants without coupling the result contract to a test framework.
package failurematrix

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"time"
)

const (
	SchemaVersion = "switchback.failure-acceptance.v1"
	ResultDirEnv  = "SWITCHBACK_FAILURE_MATRIX_RESULT_DIR"
)

var variantPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Result struct {
	SchemaVersion     string   `json:"schema_version"`
	Variant           string   `json:"variant"`
	ExecutionOutcome  string   `json:"execution_outcome"`
	AcceptanceOutcome string   `json:"acceptance_outcome"`
	Assertions        []string `json:"assertions"`
}

type Summary struct {
	SchemaVersion string    `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Outcome       string    `json:"outcome"`
	Results       []Result  `json:"results"`
}

type TestingT interface {
	Helper()
	Fatalf(string, ...any)
}

func AssertAndRecord(t TestingT, got, want Result) {
	t.Helper()
	got.SchemaVersion = SchemaVersion
	want.SchemaVersion = SchemaVersion
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failure acceptance result = %#v, want %#v", got, want)
	}
	directory := os.Getenv(ResultDirEnv)
	if directory == "" {
		return
	}
	if err := WriteResult(directory, got); err != nil {
		t.Fatalf("record failure acceptance result: %v", err)
	}
}

func WriteResult(directory string, result Result) error {
	result.SchemaVersion = SchemaVersion
	if err := validateResult(result); err != nil {
		return err
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("failure acceptance result directory must be clean and absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create failure acceptance result directory: %w", err)
	}
	return writeJSON(filepath.Join(directory, result.Variant+".json"), result)
}

func Aggregate(directory, summaryPath string, expected map[string]Result, now time.Time) (Summary, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory ||
		!filepath.IsAbs(summaryPath) || filepath.Clean(summaryPath) != summaryPath {
		return Summary{}, errors.New("failure acceptance paths must be clean and absolute")
	}
	variants := make([]string, 0, len(expected))
	for variant := range expected {
		variants = append(variants, variant)
	}
	sort.Strings(variants)
	summary := Summary{SchemaVersion: SchemaVersion, GeneratedAt: now.UTC(), Outcome: "passed"}
	for _, variant := range variants {
		content, err := os.ReadFile(filepath.Join(directory, variant+".json"))
		if err != nil {
			return Summary{}, fmt.Errorf("read %s result: %w", variant, err)
		}
		var result Result
		if err := json.Unmarshal(content, &result); err != nil {
			return Summary{}, fmt.Errorf("decode %s result: %w", variant, err)
		}
		want := expected[variant]
		want.SchemaVersion = SchemaVersion
		if err := validateResult(result); err != nil {
			return Summary{}, fmt.Errorf("validate %s result: %w", variant, err)
		}
		if !reflect.DeepEqual(result, want) {
			return Summary{}, fmt.Errorf("%s result does not match the acceptance contract", variant)
		}
		summary.Results = append(summary.Results, result)
	}
	if err := writeJSON(summaryPath, summary); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func validateResult(result Result) error {
	if result.SchemaVersion != SchemaVersion || !variantPattern.MatchString(result.Variant) ||
		result.ExecutionOutcome == "" || result.AcceptanceOutcome == "" || len(result.Assertions) == 0 {
		return errors.New("failure acceptance result is incomplete")
	}
	return nil
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create acceptance output directory: %w", err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode acceptance output: %w", err)
	}
	encoded = append(encoded, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write acceptance output: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("commit acceptance output: %w", err)
	}
	return nil
}
