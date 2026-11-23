package gatewayhttp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

func TestDeliveryReviewRouteRequiresReviewerAndUsesDeploymentScope(t *testing.T) {
	token := strings.Repeat("x", 40)
	store := &deliveryReviewTestStore{}
	handler, err := NewHandlerWithOptions(HandlerOptions{
		Actor:    FixedActor{TenantID: "tenant-local", UserID: "user-local"},
		Commands: unavailableCommands{},
		DeliveryReview: DeliveryReviewOptions{
			Store: store, WorkspaceID: "workspace-local", TokenSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(token))),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/tasks/task-example/delivery-review"
	sha := strings.Repeat("a", 64)
	input := contractsv1.DeliveryReview{
		SchemaVersion: contractsv1.DeliveryReviewSchemaVersion, ExpectedTaskRevision: 4,
		WorkerRunID: "run-example", ArtifactID: "artifact-example", ReviewRunID: "review-example",
		ArtifactSHA256: sha, ReviewResultSHA256: sha, Verdict: "passed", Findings: []contractsv1.DeliveryFinding{},
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, path, nil))
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("GET response = %d %s %s", get.Code, get.Header().Get("Cache-Control"), get.Body)
	}
	for _, item := range []struct {
		name, payload string
		bearers       []string
		want          int
	}{
		{"missing token", string(payload), nil, http.StatusUnauthorized},
		{"wrong token", string(payload), []string{"Bearer " + strings.Repeat("z", 40)}, http.StatusUnauthorized},
		{"duplicate authorization", string(payload), []string{"Bearer " + token, "Bearer " + token}, http.StatusUnauthorized},
		{"caller-supplied actor", strings.TrimSuffix(string(payload), "}") + `,"user_id":"someone-else"}`, []string{"Bearer " + token}, http.StatusBadRequest},
		{"verified reviewer", string(payload), []string{"Bearer " + token}, http.StatusOK},
	} {
		t.Run(item.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(item.payload))
			for _, bearer := range item.bearers {
				request.Header.Add("Authorization", bearer)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != item.want {
				t.Fatalf("POST response = %d %s, want %d", response.Code, response.Body, item.want)
			}
		})
	}
	want := taskapp.DeliveryReviewScope{Actor: taskapp.Actor{TenantID: "tenant-local", UserID: "user-local"}, WorkspaceID: "workspace-local", TaskID: "task-example"}
	if store.read != want || store.write != want || store.writes != 1 {
		t.Fatalf("unexpected store calls: %+v", store)
	}
}

type deliveryReviewTestStore struct {
	read, write taskapp.DeliveryReviewScope
	writes      int
}

func (store *deliveryReviewTestStore) ReadDeliveryReview(_ context.Context, scope taskapp.DeliveryReviewScope) (taskapp.CurrentDeliveryReview, error) {
	store.read = scope
	return taskapp.CurrentDeliveryReview{TaskRevision: 4}, nil
}

func (store *deliveryReviewTestStore) RecordDeliveryReview(_ context.Context, scope taskapp.DeliveryReviewScope, review contractsv1.DeliveryReview) (taskapp.DeliveryReviewReceipt, error) {
	store.write = scope
	store.writes++
	return taskapp.DeliveryReviewReceipt{DeliveryReview: review, Reviewer: "argus"}, nil
}
