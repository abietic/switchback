package gatewayhttp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	gatewayhttp "github.com/abietic/switchback/internal/gateway/http"
)

func TestHealthEndpoint(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	gatewayhttp.NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var body struct {
		Service string `json:"service"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Service != "switchback-backend" || body.Status != "ok" {
		t.Fatalf("body = %+v", body)
	}
}

func TestUnknownEndpointDoesNotExposeInternals(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/not-found", nil)
	response := httptest.NewRecorder()

	gatewayhttp.NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if body := response.Body.String(); body != "not found\n" {
		t.Fatalf("body = %q, want stable non-disclosing response", body)
	}
}
