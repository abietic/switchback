package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

const ContextPackSchemaVersion = "assistant.context_pack.v1"

type ContextCompiler struct {
	Repository ContextRepository
	IDs        IDGenerator
	Budget     int
}

func (compiler ContextCompiler) Compile(ctx context.Context, claim RunClaim) (ContextPack, error) {
	if compiler.Repository == nil || compiler.IDs == nil || compiler.Budget <= 0 ||
		claim.Run.ID == "" || claim.Run.PolicyRevision == "" || claim.Conversation.ID == "" {
		return ContextPack{}, fmt.Errorf("ContextCompiler dependencies and claim are required")
	}
	candidates, err := compiler.Repository.LoadContextCandidates(ctx, claim)
	if err != nil {
		return ContextPack{}, fmt.Errorf("load Assistant context candidates: %w", err)
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Priority != candidates[right].Priority {
			return candidates[left].Priority > candidates[right].Priority
		}
		if candidates[left].Kind != candidates[right].Kind {
			return candidates[left].Kind < candidates[right].Kind
		}
		return candidates[left].Ref < candidates[right].Ref
	})
	selected := make([]ContextItem, 0, len(candidates))
	dropped := make([]DroppedContextItem, 0)
	used := 0
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.Ref == "" || candidate.Kind == "" || candidate.EstimatedTokens < 0 ||
			!jsonObject(candidate.Content) {
			return ContextPack{}, fmt.Errorf("Context candidate %q is invalid", candidate.Ref)
		}
		key := candidate.Kind + "\x00" + candidate.Ref
		if _, duplicate := seen[key]; duplicate {
			return ContextPack{}, fmt.Errorf("Context candidate %q is duplicated", candidate.Ref)
		}
		seen[key] = struct{}{}
		if claim.Conversation.Workspace == nil && protectedContextKind(candidate.Kind) {
			dropped = append(dropped, DroppedContextItem{
				Ref: candidate.Ref, Kind: candidate.Kind, Reason: "workspace_unresolved",
			})
			continue
		}
		if used+candidate.EstimatedTokens > compiler.Budget {
			dropped = append(dropped, DroppedContextItem{
				Ref: candidate.Ref, Kind: candidate.Kind, Reason: "context_budget_exceeded",
			})
			continue
		}
		selected = append(selected, candidate)
		used += candidate.EstimatedTokens
	}
	contextPackID, err := compiler.IDs.NewID("assistant-context-pack")
	if err != nil {
		return ContextPack{}, fmt.Errorf("generate ContextPack id: %w", err)
	}
	pack := ContextPack{
		SchemaVersion: ContextPackSchemaVersion, ContextPackID: contextPackID,
		AssistantRunID: claim.Run.ID, ConversationID: claim.Conversation.ID,
		PolicyRevision: claim.Run.PolicyRevision,
		Selected:       selected, Dropped: dropped, EstimatedTokens: used, BudgetTokens: compiler.Budget,
	}
	pack.Digest, err = contextPackDigest(pack)
	if err != nil {
		return ContextPack{}, err
	}
	if err := compiler.Repository.SaveContextPack(ctx, pack); err != nil {
		return ContextPack{}, fmt.Errorf("save Assistant ContextPack: %w", err)
	}
	return pack, nil
}

func protectedContextKind(kind string) bool {
	switch kind {
	case "workspace", "repository", "truth", "inspiration", "worker_trace", "artifact":
		return true
	default:
		return false
	}
}

func contextPackDigest(pack ContextPack) (string, error) {
	pack.Digest = ""
	encoded, err := json.Marshal(pack)
	if err != nil {
		return "", fmt.Errorf("encode ContextPack digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func jsonObject(value json.RawMessage) bool {
	var object map[string]json.RawMessage
	return len(value) > 0 && json.Unmarshal(value, &object) == nil && object != nil
}
