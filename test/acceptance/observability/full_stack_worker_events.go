package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type matrixDockerEvent struct {
	ID, Project, Service, Action string
	ExecID                       string `json:"exec_id"`
	WorkerSessionID              string `json:"worker_session_id"`
	RuntimeID                    string `json:"runtime_instance_id"`
	Generation                   string `json:"runtime_generation"`
	Nano                         int64
}

func matrixOwnedEvent(project string, worker fullStackWorkerCapture, e matrixDockerEvent) (ServiceEvent, bool, error) {
	if e.ID != worker.Container.ID && e.Project != project {
		return ServiceEvent{}, false, nil
	}
	service := e.Service
	var generation uint64
	if e.ID == worker.Container.ID {
		parsed, err := strconv.ParseUint(e.Generation, 10, 64)
		if err != nil || parsed != worker.Generation || e.WorkerSessionID != worker.Session || e.RuntimeID != worker.Runtime || (e.Project != "" && e.Project != project) {
			return ServiceEvent{}, false, fmt.Errorf("resident Worker event ownership changed")
		}
		service = "resident-worker"
		generation = parsed
	} else if service == "" {
		return ServiceEvent{}, false, fmt.Errorf("owned Compose event lacks service label")
	}
	if e.ID == "" || e.Nano <= 0 || e.Action == "" {
		return ServiceEvent{}, false, fmt.Errorf("owned event identity/time missing")
	}
	action := e.Action
	for _, safe := range []string{"exec_create", "exec_start", "exec_die"} {
		if strings.HasPrefix(action, safe) {
			action = safe
			break
		}
	}
	return ServiceEvent{Project: project, Service: service, ContainerID: e.ID, At: time.Unix(0, e.Nano).UTC(), Action: action, ExecID: e.ExecID, WorkerSessionID: e.WorkerSessionID, RuntimeID: e.RuntimeID, Generation: generation}, true, nil
}

func matrixDockerIntervalCollector(project string, worker fullStackWorkerCapture) intervalCollectorIO {
	io := dockerIntervalCollector(project)
	io.worker = &worker
	io.subscribe = dockerEventSubscriber(project, &worker)
	return io
}
