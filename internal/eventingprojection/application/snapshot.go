package application

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrInvalidViewQuery             = errors.New("View query is invalid")
	ErrInvalidCommandReconciliation = errors.New("command reconciliation query is invalid")
	ErrViewUnavailable              = errors.New("View is unavailable")
)

const (
	MaxSnapshotCommandReceipts     = 64
	CommandReceiptRetentionHorizon = 30 * 24 * time.Hour
	CommandReceiptCleanupBatchSize = 256
)

type Viewer struct {
	TenantID string
	UserID   string
}

type ProjectionSnapshot struct {
	SchemaVersion     string           `json:"schema_version"`
	ViewType          string           `json:"view_type"`
	ViewID            string           `json:"view_id"`
	TenantID          string           `json:"tenant_id"`
	ViewEpoch         uint64           `json:"view_epoch"`
	ViewCursor        uint64           `json:"view_cursor"`
	ProjectionVersion uint64           `json:"projection_version"`
	QueryFingerprint  string           `json:"query_fingerprint"`
	GeneratedAt       time.Time        `json:"generated_at"`
	Data              json.RawMessage  `json:"data"`
	AvailableActions  []ActionRef      `json:"available_actions"`
	RelatedRefs       []RelatedRef     `json:"related_refs"`
	CommandReceipts   []CommandReceipt `json:"command_receipts,omitempty"`
}

type CommandReceipt struct {
	ClientCommandID string `json:"client_command_id"`
	CommandID       string `json:"command_id"`
}

func normalizeCommandReceipts(requested []string, receipts []CommandReceipt) ([]CommandReceipt, error) {
	allowed := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		allowed[value] = struct{}{}
	}
	clients := make(map[string]string, len(receipts))
	servers := make(map[string]string, len(receipts))
	for _, receipt := range receipts {
		if !canonical(receipt.ClientCommandID) || !canonical(receipt.CommandID) {
			return nil, fmt.Errorf("Snapshot command receipt is invalid")
		}
		if _, ok := allowed[receipt.ClientCommandID]; !ok {
			return nil, fmt.Errorf("Snapshot command receipt is outside the requested set")
		}
		if server, duplicate := clients[receipt.ClientCommandID]; duplicate && server != receipt.CommandID {
			return nil, fmt.Errorf("Snapshot command receipt client mapping conflicts")
		}
		if client, duplicate := servers[receipt.CommandID]; duplicate && client != receipt.ClientCommandID {
			return nil, fmt.Errorf("Snapshot command receipt server mapping conflicts")
		}
		clients[receipt.ClientCommandID] = receipt.CommandID
		servers[receipt.CommandID] = receipt.ClientCommandID
	}
	normalized := make([]CommandReceipt, 0, len(clients))
	for client, server := range clients {
		normalized = append(normalized, CommandReceipt{ClientCommandID: client, CommandID: server})
	}
	sort.Slice(normalized, func(left, right int) bool {
		if normalized[left].ClientCommandID == normalized[right].ClientCommandID {
			return normalized[left].CommandID < normalized[right].CommandID
		}
		return normalized[left].ClientCommandID < normalized[right].ClientCommandID
	})
	return normalized, nil
}

// CommandReconciliationDigest binds a cache representation to both the
// normalized requested client identities and the exact returned mappings.
func CommandReconciliationDigest(
	requested []string,
	receipts []CommandReceipt,
) (string, error) {
	normalizedRequested, err := normalizeCommandReceiptIDs(requested)
	if err != nil {
		return "", err
	}
	normalizedReceipts, err := normalizeCommandReceipts(normalizedRequested, receipts)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Requested []string         `json:"requested"`
		Receipts  []CommandReceipt `json:"receipts"`
	}{
		Requested: normalizedRequested,
		Receipts:  normalizedReceipts,
	})
	if err != nil {
		return "", fmt.Errorf("encode command reconciliation representation: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func normalizeCommandReceiptIDs(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if !canonical(value) {
			return nil, ErrInvalidCommandReconciliation
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
		if len(normalized) > MaxSnapshotCommandReceipts {
			return nil, ErrInvalidCommandReconciliation
		}
	}
	sort.Strings(normalized)
	return normalized, nil
}
