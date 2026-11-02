// Package upload sends finalized AgentTurn evidence to the Backend.
package upload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
	workerevidence "switchback.local/spikes/worker-runtime/worker/internal/evidence"
)

const (
	HeaderBeforeTree = sharedevidence.HeaderBeforeTree
	HeaderAfterTree  = sharedevidence.HeaderAfterTree

	FailureCodeEvidenceUpload = "evidence_upload_failed"

	maxResponseBytes = 64 << 10
)

type TerminalSink interface {
	TurnCompleted(context.Context, sharedevidence.TurnIdentity, []sharedevidence.ObjectRef) error
	TurnFailed(context.Context, sharedevidence.TurnIdentity, string, string, []sharedevidence.ObjectRef) error
}

type Client struct {
	baseURL     *url.URL
	workerToken string
	httpClient  *http.Client
}

func NewClient(baseURL, workerToken string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse Backend URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("Backend URL must be an absolute HTTP URL without userinfo, query, or fragment")
	}
	if workerToken == "" || strings.ContainsAny(workerToken, " \t\r\n") {
		return nil, fmt.Errorf("Worker token is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:     parsed,
		workerToken: workerToken,
		httpClient:  httpClient,
	}, nil
}

func (c *Client) Finalize(
	ctx context.Context,
	bundle workerevidence.Bundle,
	sink TerminalSink,
) error {
	if c == nil || c.baseURL == nil || c.httpClient == nil {
		return fmt.Errorf("upload client is not initialized")
	}
	if sink == nil {
		return fmt.Errorf("terminal sink is required")
	}
	refs, err := c.uploadAll(ctx, bundle)
	if err != nil {
		failureErr := sink.TurnFailed(
			ctx,
			bundle.Identity,
			FailureCodeEvidenceUpload,
			"AgentTurn evidence upload or verification failed",
			refs,
		)
		if failureErr != nil {
			return errors.Join(err, fmt.Errorf("emit TurnFailed: %w", failureErr))
		}
		return err
	}
	if err := sink.TurnCompleted(ctx, bundle.Identity, refs); err != nil {
		return fmt.Errorf("emit TurnCompleted: terminality is unknown: %w", err)
	}
	return nil
}

func (c *Client) uploadAll(
	ctx context.Context,
	bundle workerevidence.Bundle,
) ([]sharedevidence.ObjectRef, error) {
	if err := validateBundle(bundle); err != nil {
		return nil, err
	}
	refs := make([]sharedevidence.ObjectRef, 0, len(bundle.Objects))
	for _, object := range bundle.Objects {
		ref, err := c.uploadOne(ctx, bundle, object)
		if err != nil {
			return refs, fmt.Errorf("upload %s: %w", object.Kind, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (c *Client) uploadOne(
	ctx context.Context,
	bundle workerevidence.Bundle,
	object workerevidence.Object,
) (sharedevidence.ObjectRef, error) {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") +
		"/internal/v1/sessions/" + url.PathEscape(bundle.Identity.WorkerSessionID) +
		"/runs/" + url.PathEscape(bundle.Identity.WorkerRunID) +
		"/turns/" + url.PathEscape(bundle.Identity.AgentTurnID) +
		"/evidence/" + url.PathEscape(object.Kind)

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPut,
		endpoint.String(),
		bytes.NewReader(object.Payload),
	)
	if err != nil {
		return sharedevidence.ObjectRef{}, fmt.Errorf("create request")
	}
	request.Header.Set("Authorization", "Bearer "+c.workerToken)
	request.Header.Set("Content-Type", object.ContentType)
	request.Header.Set("X-Content-SHA256", object.SHA256)
	request.Header.Set(HeaderBeforeTree, bundle.BeforeTree)
	request.Header.Set(HeaderAfterTree, bundle.AfterTree)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return sharedevidence.ObjectRef{}, fmt.Errorf("request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return sharedevidence.ObjectRef{}, fmt.Errorf("Backend returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return sharedevidence.ObjectRef{}, fmt.Errorf("read Backend object ref")
	}
	if len(body) > maxResponseBytes {
		return sharedevidence.ObjectRef{}, fmt.Errorf("Backend object ref exceeds response limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var ref sharedevidence.ObjectRef
	if err := decoder.Decode(&ref); err != nil {
		return sharedevidence.ObjectRef{}, fmt.Errorf("decode Backend object ref")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return sharedevidence.ObjectRef{}, fmt.Errorf("Backend object ref has trailing content")
	}
	if err := validateBackendRef(object.Kind, ref); err != nil {
		return sharedevidence.ObjectRef{}, err
	}
	return ref, nil
}

func validateBundle(bundle workerevidence.Bundle) error {
	identities := []string{
		bundle.Identity.WorkerSessionID,
		bundle.Identity.WorkerRunID,
		bundle.Identity.RuntimeInstanceID,
		bundle.Identity.AgentSessionID,
		bundle.Identity.AgentTurnID,
		bundle.Identity.TraceID,
	}
	for _, identity := range identities {
		if !safePathSegment(identity) {
			return fmt.Errorf("bundle contains an invalid identity")
		}
	}
	if bundle.Identity.RuntimeGeneration == 0 {
		return fmt.Errorf("bundle runtime generation is required")
	}
	if !validTreeID(bundle.BeforeTree) {
		return fmt.Errorf("bundle before tree is invalid")
	}
	if !validTreeID(bundle.AfterTree) {
		return fmt.Errorf("bundle after tree is invalid")
	}
	wantKinds := []string{
		sharedevidence.KindReport,
		sharedevidence.KindRawACP,
		sharedevidence.KindTrace,
		sharedevidence.KindPatch,
	}
	if len(bundle.Objects) != len(wantKinds) {
		return fmt.Errorf("bundle requires exactly four evidence objects")
	}
	for index, object := range bundle.Objects {
		if object.Kind != wantKinds[index] {
			return fmt.Errorf("bundle object %d kind = %q, want %q", index, object.Kind, wantKinds[index])
		}
		if strings.TrimSpace(object.ContentType) == "" {
			return fmt.Errorf("bundle object %q content type is required", object.Kind)
		}
		if object.Bytes != int64(len(object.Payload)) ||
			object.SHA256 != contentSHA256(object.Payload) {
			return fmt.Errorf("bundle object %q metadata does not match payload", object.Kind)
		}
	}
	return nil
}

func validateBackendRef(wantKind string, ref sharedevidence.ObjectRef) error {
	if ref.Kind != wantKind {
		return fmt.Errorf("Backend object ref kind = %q, want %q", ref.Kind, wantKind)
	}
	if strings.TrimSpace(ref.Path) == "" {
		return fmt.Errorf("Backend object ref path is required")
	}
	if !validSHA(ref.SHA256) {
		return fmt.Errorf("Backend object ref SHA256 is invalid")
	}
	if ref.Bytes < 0 {
		return fmt.Errorf("Backend object ref bytes is negative")
	}
	return nil
}

func safePathSegment(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/\\\r\n\t ")
}

func validTreeID(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validLowerHex(value)
}

func validSHA(value string) bool {
	return len(value) == sha256.Size*2 && validLowerHex(value)
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}

func contentSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
