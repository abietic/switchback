package gatewayhttp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	taskapp "github.com/abietic/switchback/internal/taskorchestration/application"
)

type DeliveryReviewOptions struct {
	Store       taskapp.DeliveryReviewStore
	WorkspaceID string
	TokenSHA256 string
}

func (options DeliveryReviewOptions) validate() error {
	if options.Store == nil && options.TokenSHA256 == "" {
		return nil
	}
	if options.Store == nil || !canonical(options.WorkspaceID) {
		return fmt.Errorf("delivery review store and deployment Workspace are required")
	}
	if options.TokenSHA256 != "" {
		digest, err := hex.DecodeString(options.TokenSHA256)
		if err != nil || len(digest) != sha256.Size || options.TokenSHA256 != strings.ToLower(options.TokenSHA256) {
			return fmt.Errorf("delivery review token SHA-256 must be 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

func registerDeliveryReview(mux *http.ServeMux, actor FixedActor, options DeliveryReviewOptions) {
	if options.Store == nil {
		return
	}
	scopeFor := func(request *http.Request) taskapp.DeliveryReviewScope {
		return taskapp.DeliveryReviewScope{
			Actor:       taskapp.Actor{TenantID: actor.TenantID, UserID: actor.UserID},
			WorkspaceID: options.WorkspaceID, TaskID: request.PathValue("task_id"),
		}
	}
	mux.HandleFunc("GET /api/v1/tasks/{task_id}/delivery-review", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "private, no-store")
		scope := scopeFor(request)
		if !canonical(scope.TaskID) {
			writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
			return
		}
		result, err := options.Store.ReadDeliveryReview(request.Context(), scope)
		if err != nil {
			writeDeliveryReviewError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
	})
	if options.TokenSHA256 == "" {
		return
	}
	digest, _ := hex.DecodeString(options.TokenSHA256)
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/delivery-review", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "private, no-store")
		scheme, token, _ := strings.Cut(request.Header.Get("Authorization"), " ")
		provided := sha256.Sum256([]byte(token))
		if len(request.Header.Values("Authorization")) != 1 || !strings.EqualFold(scheme, "Bearer") ||
			len(token) < 32 || strings.ContainsAny(token, " \t\r\n") || subtle.ConstantTimeCompare(provided[:], digest) != 1 {
			response.Header().Set("WWW-Authenticate", "Bearer")
			writeCommandError(response, http.StatusUnauthorized, "reviewer_unauthorized", "")
			return
		}
		scope := scopeFor(request)
		body, bodyErr := readBody(response, request)
		review, err := contractsv1.DecodeDeliveryReview(body)
		if bodyErr != nil || err != nil || !canonical(scope.TaskID) {
			writeCommandError(response, http.StatusBadRequest, "invalid_request", "")
			return
		}
		receipt, err := options.Store.RecordDeliveryReview(request.Context(), scope, review)
		if err != nil {
			writeDeliveryReviewError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, receipt)
	})
}

func writeDeliveryReviewError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, taskapp.ErrDeliveryReviewUnavailable):
		writeCommandError(response, http.StatusNotFound, "target_unavailable", "")
	case errors.Is(err, taskapp.ErrDeliveryReviewStale):
		writeCommandError(response, http.StatusConflict, "stale_target", "")
	case errors.Is(err, taskapp.ErrIdempotencyConflict):
		writeCommandError(response, http.StatusConflict, "review_conflict", "")
	default:
		writeCommandError(response, http.StatusServiceUnavailable, "command_service_unavailable", "")
	}
}
