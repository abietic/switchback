package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"switchback.local/spikes/worker-runtime/backend/internal/objectstore"
	sharedevidence "switchback.local/spikes/worker-runtime/internal/evidence"
)

const internalUploadLimit = 32 << 20

func (h *Handler) uploadEvidence(writer http.ResponseWriter, request *http.Request, parts []string) {
	token, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		writeError(writer, http.StatusUnauthorized, "worker authentication failed")
		return
	}
	authorization, err := h.coordinator.AuthorizeEvidenceUpload(parts[3], parts[5], parts[7], token)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	kind, err := objectstore.ParseUploadEvidenceKind(parts[9])
	if err != nil {
		writeError(writer, http.StatusBadRequest, "unsupported evidence kind")
		return
	}
	if strings.TrimSpace(request.Header.Get("Content-Type")) == "" {
		writeError(writer, http.StatusBadRequest, "content type is required")
		return
	}
	trees := sharedevidence.TreePair{
		BeforeTree: request.Header.Get(sharedevidence.HeaderBeforeTree),
		AfterTree:  request.Header.Get(sharedevidence.HeaderAfterTree),
	}
	if !validTreeID(trees.BeforeTree) || !validTreeID(trees.AfterTree) {
		writeError(writer, http.StatusBadRequest, "valid before and after tree IDs are required")
		return
	}
	expectedDigestHex := request.Header.Get("X-Content-SHA256")
	if len(expectedDigestHex) != sha256.Size*2 {
		writeError(writer, http.StatusBadRequest, "invalid content checksum")
		return
	}
	expectedDigest, err := hex.DecodeString(expectedDigestHex)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid content checksum")
		return
	}
	if request.ContentLength > internalUploadLimit {
		writeError(writer, http.StatusRequestEntityTooLarge, "evidence payload exceeds limit")
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, internalUploadLimit+1))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "unable to read evidence payload")
		return
	}
	if len(payload) > internalUploadLimit {
		writeError(writer, http.StatusRequestEntityTooLarge, "evidence payload exceeds limit")
		return
	}
	actual := sha256.Sum256(payload)
	if subtle.ConstantTimeCompare(expectedDigest, actual[:]) != 1 {
		writeError(writer, http.StatusBadRequest, "evidence checksum mismatch")
		return
	}
	stored, err := h.coordinator.StoreEvidenceWithTrees(
		request.Context(),
		authorization,
		kind,
		payload,
		trees,
	)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, sharedevidence.ObjectRef{
		Kind:   string(kind),
		Path:   stored.Path,
		SHA256: stored.SHA256,
		Bytes:  stored.Bytes,
	})
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(header, prefix)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func validTreeID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || (character > '9' && character < 'a') || character > 'f' {
			return false
		}
	}
	return true
}
