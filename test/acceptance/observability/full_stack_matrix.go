package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

type fullStackMatrixWindow struct {
	Original  string `json:"original"`
	Recovered string `json:"recovered"`
	Intervals string `json:"intervals"`
}

type fullStackMatrixPair struct {
	Healthy fullStackMatrixWindow `json:"healthy"`
	Outage  fullStackMatrixWindow `json:"outage"`
}

type fullStackMatrixRecipe struct {
	Schema        string                `json:"schema_version"`
	Project       string                `json:"project"`
	Smoke         string                `json:"smoke"`
	Started       string                `json:"started"`
	Initial       string                `json:"initial_capture"`
	Final         string                `json:"final_capture"`
	WorkerInitial string                `json:"initial_worker_capture"`
	WorkerFinal   string                `json:"final_worker_capture"`
	Pairs         []fullStackMatrixPair `json:"pairs"`
}

type fullStackWorkerCapture struct {
	Container  CapturedContainer `json:"container"`
	Labels     map[string]string `json:"identity_labels"`
	Session    string            `json:"worker_session_id"`
	Runtime    string            `json:"runtime_instance_id"`
	Generation uint64            `json:"runtime_generation"`
	ObservedAt time.Time         `json:"observed_at"`
}

func fullStackMatrixEnvironment(project, output, backend string) error {
	values, err := correlationEnvironment(project, output)
	if err != nil {
		return err
	}
	u, err := url.Parse(backend)
	if err != nil || u.Scheme != "http" || u.User != nil || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() != values["SWITCHBACK_BACKEND_PORT"] || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("full-stack matrix Backend does not match claim")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("OBSERVABILITY_DATABASE_URL"))
	if err != nil || cfg.ConnConfig.Database != values["OBSERVABILITY_DATABASE_NAME"] || cfg.ConnConfig.User != "switchback" || cfg.ConnConfig.Password != values["SWITCHBACK_POSTGRES_PASSWORD"] || net.ParseIP(cfg.ConnConfig.Host) == nil || !net.ParseIP(cfg.ConnConfig.Host).IsLoopback() || fmt.Sprint(cfg.ConnConfig.Port) != values["OBSERVABILITY_POSTGRES_PORT"] || len(cfg.ConnConfig.Fallbacks) != 0 {
		return fmt.Errorf("full-stack matrix database does not match exact claim")
	}
	return nil
}

func captureFullStackMatrixProject(ctx context.Context, project, backend string) (ProjectCapture, error) {
	capture := ProjectCapture{Schema: "observability.project_capture.v1", Project: project, StartedAt: time.Now().UTC(), Containers: []CapturedContainer{}}
	containers, err := inspectOwnedServiceContainers(ctx, project)
	if err != nil {
		return capture, err
	}
	for _, c := range containers {
		capture.Containers = append(capture.Containers, c)
	}
	sort.Slice(capture.Containers, func(i, j int) bool { return capture.Containers[i].Service < capture.Containers[j].Service })
	for _, name := range append([]string{"switchback-backend", "postgres", "observation-fake-model"}, fullStackObservationServices...) {
		if _, ok := containers[name]; !ok {
			capture.AbsentServices = append(capture.AbsentServices, name)
		}
	}
	if c := containers["switchback-backend"]; c.Running {
		p := &HTTPHealthProbe{URL: backend + "/healthz", StartedAt: time.Now().UTC()}
		client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, e := client.Get(p.URL)
		if e != nil {
			p.Error = true
		} else {
			p.Status = response.StatusCode
			_ = response.Body.Close()
		}
		p.CompletedAt = time.Now().UTC()
		capture.BackendHealth = p
	}
	capture.CompletedAt = time.Now().UTC()
	return capture, nil
}

func (d *matrixDriver) captureFullStackWorker(ctx context.Context) (fullStackWorkerCapture, error) {
	c := fullStackWorkerCapture{Session: d.session, Runtime: d.runtime, Generation: d.generation, ObservedAt: time.Now().UTC()}
	var id, status string
	if err := d.pool.QueryRow(ctx, `SELECT COALESCE(container_id,''),status FROM runtime_instances WHERE runtime_instance_id=$1 AND worker_session_id=$2 AND generation=$3`, d.runtime, d.session, d.generation).Scan(&id, &status); err != nil || id == "" || status != "active" {
		return c, fmt.Errorf("resident Worker database identity unavailable: %w", err)
	}
	raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", inspectTemplate, id).Output()
	if err != nil || json.Unmarshal(raw, &c.Container) != nil || c.Container.ID != id || !c.Container.Running || c.Container.StartedAt.IsZero() {
		return c, fmt.Errorf("resident Worker exact container unavailable: %w", err)
	}
	labelsRaw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{json .Config.Labels}}`, id).Output()
	var labels map[string]string
	if err != nil || json.Unmarshal(labelsRaw, &labels) != nil || labels["com.switchback.worker-session-id"] != d.session || labels["com.switchback.runtime-instance-id"] != d.runtime || labels["com.switchback.runtime-generation"] != fmt.Sprint(d.generation) {
		return c, fmt.Errorf("resident Worker container labels mismatch")
	}
	c.Labels = map[string]string{"com.switchback.worker-session-id": labels["com.switchback.worker-session-id"], "com.switchback.runtime-instance-id": labels["com.switchback.runtime-instance-id"], "com.switchback.runtime-generation": labels["com.switchback.runtime-generation"]}
	c.ObservedAt = time.Now().UTC()
	return c, nil
}

func (d *matrixDriver) measureFullStack(ctx context.Context, nonce, state string, worker fullStackWorkerCapture) (RawEvidence, string, error) {
	io := matrixDockerIntervalCollector(d.project, worker)
	io.probe = probeFullStackServices
	raw, intervals, err := measureWithIntervalsForServices(ctx, d.project, state, fullStackObservationServices, io, func() (RawEvidence, error) { return d.measureRaw(ctx, nonce) })
	name := ""
	if len(intervals.Subscriptions) > 0 {
		name = "intervals-" + intervals.Subscriptions[0].ID + ".json"
		err = errors.Join(err, writeEvidence(d.output, name, intervals))
	}
	return raw, name, err
}

func runFullStackMatrix(project, output, backend string) error {
	if err := fullStackMatrixEnvironment(project, output, backend); err != nil {
		return err
	}
	ctx := context.Background()
	d, err := openMatrixDriver(ctx, project, output, backend)
	if err != nil {
		return err
	}
	defer d.pool.Close()
	defer d.proofPool.Close()
	read := func(name string, dst any) error {
		data, e := readFullStackInput(output, name)
		if e != nil {
			return e
		}
		return json.Unmarshal(data, dst)
	}
	var smoke RawEvidence
	if err = read("smoke-passed.json", &smoke); err != nil {
		return fmt.Errorf("verified smoke required: %w", err)
	}
	sample, err := supplementEvidenceSample(smoke, smoke)
	if err != nil || smoke.Project != project {
		return fmt.Errorf("smoke source/claim invalid: %w", err)
	}
	d.session, d.runtime, d.generation = sample.Measurement.SessionID, sample.Measurement.RuntimeID, sample.Measurement.Generation
	fixture, err := exactServiceContainer(ctx, project, "observation-fake-model")
	if err != nil || !fixture.Running {
		return fmt.Errorf("controlled fixture identity unavailable: %w", err)
	}
	d.fixture = fixture.ID
	initial, err := captureFullStackMatrixProject(ctx, project, backend)
	if err != nil {
		return err
	}
	if err = verifyMatrixCapture(initial, initial, smoke, fixture.ID); err != nil {
		return err
	}
	workerInitial, err := d.captureFullStackWorker(ctx)
	if err != nil {
		return err
	}
	started := struct {
		Project, Session, Runtime, Fixture string
		Generation                         uint64
		At                                 time.Time
	}{project, d.session, d.runtime, d.fixture, d.generation, time.Now().UTC()}
	if err = writeEvidence(output, "full-stack-matrix-started.json", started); err != nil {
		return fmt.Errorf("matrix claim already started or unwritable: %w", err)
	}
	if err = writeEvidence(output, "full-stack-matrix-initial.json", initial); err != nil {
		return err
	}
	if err = writeEvidence(output, "full-stack-matrix-worker-initial.json", workerInitial); err != nil {
		return err
	}
	recipe := fullStackMatrixRecipe{Schema: "observability.full_stack_matrix_recipe.v1", Project: project, Smoke: "smoke-passed.json", Started: "full-stack-matrix-started.json", Initial: "full-stack-matrix-initial.json", Final: "full-stack-matrix-final.json", WorkerInitial: "full-stack-matrix-worker-initial.json", WorkerFinal: "full-stack-matrix-worker-final.json"}
	for i := 0; i < 20; i++ {
		pair := fullStackMatrixPair{}
		for _, state := range []string{"healthy", "unavailable"} {
			bounded, done := context.WithTimeout(ctx, 5*time.Minute)
			action := "start"
			if state == "unavailable" {
				action = "stop"
			}
			err = d.compose(bounded, action, fullStackObservationServices)
			if err == nil {
				err = d.waitFullStackServices(bounded, state)
			}
			nonce := fmt.Sprintf("%sf%02d%s", strings.TrimPrefix(project, "switchback-obsv-"), i, map[string]string{"healthy": "h", "unavailable": "o"}[state])
			var original RawEvidence
			var intervalName string
			if err == nil {
				original, intervalName, err = d.measureFullStack(bounded, nonce, state, workerInitial)
			}
			if err == nil && state == "unavailable" {
				err = d.compose(bounded, "start", fullStackObservationServices)
				if err == nil {
					err = d.waitFullStackServices(bounded, "healthy")
				}
			}
			if err == nil {
				_, err = d.recoverSample(bounded, original)
			}
			done()
			if err != nil {
				return fmt.Errorf("full-stack matrix pair %d %s: %w", i, state, err)
			}
			window := fullStackMatrixWindow{Original: "sample-" + nonce + "-original.json", Recovered: "sample-" + nonce + "-recovered.json", Intervals: intervalName}
			if state == "healthy" {
				pair.Healthy = window
			} else {
				pair.Outage = window
			}
		}
		recipe.Pairs = append(recipe.Pairs, pair)
		fmt.Printf("full_stack_matrix_pair=%d/20 complete\n", i+1)
	}
	final, err := captureFullStackMatrixProject(ctx, project, backend)
	if err != nil {
		return err
	}
	workerFinal, err := d.captureFullStackWorker(ctx)
	if err != nil {
		return err
	}
	if err = verifyMatrixWorkerCapture(workerInitial, workerFinal, d.session, d.runtime, d.generation); err != nil {
		return err
	}
	if err = writeEvidence(output, recipe.Final, final); err != nil {
		return err
	}
	if err = writeEvidence(output, recipe.WorkerFinal, workerFinal); err != nil {
		return err
	}
	if err = writeEvidence(output, "full-stack-matrix-recipe.json", recipe); err != nil {
		return err
	}
	return nil
}
