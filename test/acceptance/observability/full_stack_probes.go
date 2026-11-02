package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"
)

// The four extra services have no reliable in-container tool contract. Probe
// them from the owned fixture's Node client, targeting the exact inspected IP.
func probeFullStackServices(ctx context.Context, project string) ([]ServiceProbe, error) {
	containers, err := inspectOwnedServiceContainers(ctx, project)
	if err != nil {
		return nil, err
	}
	probes := make([]ServiceProbe, len(fullStackObservationServices))
	fixture := containers["observation-fake-model"]
	var wg sync.WaitGroup
	probeErrors := make(chan error, 5)
	wg.Add(1)
	go func() {
		defer wg.Done()
		legacy, e := probeObservationServices(ctx, project)
		if e != nil {
			probeErrors <- e
			return
		}
		if len(legacy) != len(observationServices) {
			probeErrors <- fmt.Errorf("legacy probe set incomplete")
			return
		}
		for i, p := range legacy {
			c := containers[p.Service]
			if p.ContainerID != c.ID || !p.StartedAt.Equal(c.StartedAt) {
				probeErrors <- fmt.Errorf("service identity changed during probe")
				return
			}
			probes[i] = p
		}
	}()
	for i, spec := range []struct {
		service, port, path string
	}{{"prometheus", "9090", "/-/ready"}, {"alloy", "12346", "/-/ready"}, {"loki", "3100", "/ready"}, {"grafana", "3000", "/api/health"}} {
		wg.Add(1)
		go func(i int, spec struct{ service, port, path string }) {
			defer wg.Done()
			c, exists := containers[spec.service]
			p := ServiceProbe{Project: project, Service: spec.service, ContainerID: c.ID, StartedAt: c.StartedAt, From: time.Now().UTC(), State: "unavailable", Kind: "exact_inspect"}
			if exists && c.Running {
				p.State, p.Kind = "unknown", "independent_health_probe"
				if !fixture.Running || fixture.ID == "" {
					probeErrors <- fmt.Errorf("controlled fixture unavailable for health probe")
					return
				}
				probeCtx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
				defer cancel()
				ip, e := sharedFixtureNetworkIP(probeCtx, fixture.ID, c.ID)
				if e != nil {
					probeErrors <- e
					return
				}
				// The fixed URL, manual redirect mode and exact IP bind the HTTP
				// response to the inspected container, not mutable service DNS.
				script := `fetch(process.argv[1],{redirect:'manual',signal:AbortSignal.timeout(2000),headers:{host:process.argv[2]}}).then(r=>process.exit(r.status===200?0:1)).catch(()=>process.exit(1))`
				endpoint := "http://" + net.JoinHostPort(ip, spec.port) + spec.path
				e = exec.CommandContext(probeCtx, "docker", "exec", fixture.ID, "node", "-e", script, endpoint, spec.service+":"+spec.port).Run()
				if e == nil {
					p.State = "healthy"
				}
			}
			p.To = time.Now().UTC()
			probes[len(observationServices)+i] = p
		}(i, spec)
	}
	wg.Wait()
	close(probeErrors)
	for e := range probeErrors {
		if e != nil {
			return nil, e
		}
	}
	return probes, nil
}

func sharedFixtureNetworkIP(ctx context.Context, fixtureID, targetID string) (string, error) {
	type network struct {
		IP string `json:"IPAddress"`
	}
	read := func(id string) (map[string]network, error) {
		raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{{json .NetworkSettings.Networks}}`, id).Output()
		if err != nil {
			return nil, fmt.Errorf("exact service network inspection failed")
		}
		var networks map[string]network
		if json.Unmarshal(raw, &networks) != nil {
			return nil, fmt.Errorf("exact service network invalid")
		}
		return networks, nil
	}
	fixture, err := read(fixtureID)
	if err != nil {
		return "", err
	}
	target, err := read(targetID)
	if err != nil {
		return "", err
	}
	for name, endpoint := range target {
		if _, ok := fixture[name]; ok && net.ParseIP(endpoint.IP) != nil {
			return endpoint.IP, nil
		}
	}
	return "", fmt.Errorf("service has no exact shared fixture network endpoint")
}

func (d *matrixDriver) waitFullStackServices(ctx context.Context, state string) error {
	for {
		probes, err := probeFullStackServices(ctx, d.project)
		if err != nil {
			return err
		}
		ready := len(probes) == len(fullStackObservationServices)
		for _, p := range probes {
			ready = ready && p.State == state
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("nine-service state %s not reached: %w", state, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}
