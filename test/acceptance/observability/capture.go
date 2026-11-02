package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Deliberately omit Env, command, mounts, labels, health logs and raw inspect data.
const inspectTemplate = `{"id":{{json .Id}},"name":{{json .Name}},"created_at":{{json .Created}},"image_id":{{json .Image}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"status":{{json .State.Status}},"running":{{json .State.Running}},"started_at":{{json .State.StartedAt}},"finished_at":{{json .State.FinishedAt}},"exit_code":{{json .State.ExitCode}},"health":{{with (index .State "Health")}}{{json (index . "Status")}}{{else}}null{{end}}}`

type CapturedContainer struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	ImageID    string    `json:"image_id"`
	Project    string    `json:"project"`
	Service    string    `json:"service"`
	Status     string    `json:"status"`
	Running    bool      `json:"running"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	ExitCode   int       `json:"exit_code"`
	Health     *string   `json:"health"`
}
type ProjectCapture struct {
	BackendHealth  *HTTPHealthProbe    `json:"backend_http_health,omitempty"`
	Schema         string              `json:"schema_version"`
	Project        string              `json:"project"`
	StartedAt      time.Time           `json:"capture_started_at"`
	CompletedAt    time.Time           `json:"capture_completed_at"`
	Containers     []CapturedContainer `json:"containers"`
	AbsentServices []string            `json:"absent_services"`
}

type HTTPHealthProbe struct {
	URL         string    `json:"url"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Status      int       `json:"status"`
	Error       bool      `json:"transport_error"`
}

func verifyColdStart(before, after ProjectCapture) error {
	return verifyColdStartForServices(before, after, observationServices)
}

func verifyColdStartForServices(before, after ProjectCapture, services []string) error {
	if before.Schema != "observability.project_capture.v1" || after.Schema != before.Schema || before.Project == "" || before.Project != after.Project || before.CompletedAt.IsZero() || after.StartedAt.Before(before.CompletedAt) {
		return fmt.Errorf("cold capture identity/order invalid")
	}
	for _, capture := range []ProjectCapture{before, after} {
		if capture.StartedAt.IsZero() || capture.CompletedAt.Before(capture.StartedAt) {
			return fmt.Errorf("cold capture timing invalid")
		}
		absent := map[string]bool{}
		for _, service := range capture.AbsentServices {
			if len(services) > len(observationServices) && absent[service] {
				return fmt.Errorf("duplicate absent service capture")
			}
			absent[service] = true
		}
		if len(services) > len(observationServices) {
			containers, ids := map[string]bool{}, map[string]bool{}
			for _, c := range capture.Containers {
				if c.Project != capture.Project || c.Service == "" || c.ID == "" || containers[c.Service] || ids[c.ID] || absent[c.Service] {
					return fmt.Errorf("duplicate or contradictory container capture")
				}
				containers[c.Service], ids[c.ID] = true, true
			}
		}
		for _, service := range services {
			found := absent[service]
			for _, absent := range capture.AbsentServices {
				if absent == service {
					found = true
				}
			}
			for _, c := range capture.Containers {
				if c.Service == service {
					if c.Running {
						return fmt.Errorf("observation service running during cold proof")
					}
					found = true
				}
			}
			if !found {
				return fmt.Errorf("observation service coverage missing")
			}
		}
	}
	for _, c := range before.Containers {
		if c.Service == "switchback-backend" {
			return fmt.Errorf("cold proof requires Backend absent before process creation")
		}
	}
	healthyBackend, healthyCore := false, false
	for _, c := range after.Containers {
		if c.Service == "switchback-backend" {
			healthyBackend = c.Running && c.CreatedAt.After(before.CompletedAt) && c.StartedAt.After(before.CompletedAt)
		}
		if c.Service == "postgres" {
			healthyCore = c.Running && c.Health != nil && *c.Health == "healthy"
		}
	}
	if after.BackendHealth == nil || after.BackendHealth.Status != http.StatusOK || after.BackendHealth.Error || after.BackendHealth.StartedAt.Before(after.StartedAt) || after.BackendHealth.CompletedAt.After(after.CompletedAt) {
		healthyBackend = false
	}
	if !healthyBackend || !healthyCore {
		return fmt.Errorf("cold Backend/core health or creation proof missing")
	}
	return nil
}

func captureProject(project, output string) error {
	if !projectPattern.MatchString(project) {
		return fmt.Errorf("unsafe project")
	}
	data, err := os.ReadFile(filepath.Join(output, "claim.json"))
	if err != nil {
		return err
	}
	var claim map[string]string
	if err = json.Unmarshal(data, &claim); err != nil {
		return err
	}
	if claim["project"] != project || claim["output"] != output {
		return fmt.Errorf("claim mismatch")
	}
	capture := ProjectCapture{Schema: "observability.project_capture.v1", Project: project, StartedAt: time.Now().UTC(), Containers: []CapturedContainer{}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// These two commands are read-only and bounded to a validated exact compose project.
	list, err := exec.CommandContext(ctx, "docker", "ps", "--all", "--no-trunc", "--filter", "label=com.docker.compose.project="+project, "--format", "{{.ID}}").Output()
	if err != nil {
		return fmt.Errorf("list owned project: %w", err)
	}
	ids := strings.Fields(string(list))
	for _, id := range ids {
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
			return fmt.Errorf("noncanonical container id")
		}
		raw, e := exec.CommandContext(ctx, "docker", "inspect", "--format", inspectTemplate, id).Output()
		if e != nil {
			return fmt.Errorf("inspect selected owned container: %w", e)
		}
		var container CapturedContainer
		if e = json.Unmarshal(raw, &container); e != nil {
			return e
		}
		if container.Project != project || container.ID != id {
			return fmt.Errorf("container ownership changed during capture")
		}
		capture.Containers = append(capture.Containers, container)
	}
	services := map[string]bool{}
	for _, c := range capture.Containers {
		services[c.Service] = true
	}
	for _, c := range capture.Containers {
		if c.Service == "switchback-backend" && c.Running {
			envData, e := os.ReadFile(filepath.Join(output, "acceptance.env"))
			if e != nil {
				return e
			}
			port := ""
			for _, line := range strings.Split(string(envData), "\n") {
				if strings.HasPrefix(line, "SWITCHBACK_BACKEND_PORT=") {
					port = strings.TrimPrefix(line, "SWITCHBACK_BACKEND_PORT=")
				}
			}
			if !regexp.MustCompile(`^[0-9]{1,5}$`).MatchString(port) {
				return fmt.Errorf("claimed Backend port missing")
			}
			probe := &HTTPHealthProbe{URL: "http://127.0.0.1:" + port + "/healthz", StartedAt: time.Now().UTC()}
			client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, e := client.Get(probe.URL)
			if e != nil {
				probe.Error = true
			} else {
				probe.Status = response.StatusCode
				_ = response.Body.Close()
			}
			probe.CompletedAt = time.Now().UTC()
			capture.BackendHealth = probe
		}
	}
	for _, service := range append([]string{"switchback-backend", "postgres"}, fullStackObservationServices...) {
		if !services[service] {
			capture.AbsentServices = append(capture.AbsentServices, service)
		}
	}
	capture.CompletedAt = time.Now().UTC()
	encoded, err := json.MarshalIndent(capture, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(output, fmt.Sprintf("capture-%d.json", capture.CompletedAt.UnixNano())), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(encoded)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	fmt.Println(file.Name())
	return nil
}
