package application

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/abietic/switchback/internal/interaction/domain"
)

type contextRepository struct {
	candidates []ContextItem
	saved      []ContextPack
}

func (repository *contextRepository) LoadContextCandidates(context.Context, RunClaim) ([]ContextItem, error) {
	return append([]ContextItem(nil), repository.candidates...), nil
}

func (repository *contextRepository) SaveContextPack(_ context.Context, pack ContextPack) error {
	repository.saved = append(repository.saved, pack)
	return nil
}

type contextIDs int

func (ids *contextIDs) NewID(string) (string, error) {
	*ids++
	return "context-pack-1", nil
}

func TestContextCompilerSelectsDeterministicallyAndRecordsBudgetDrops(t *testing.T) {
	repository := &contextRepository{candidates: []ContextItem{
		{Ref: "turn-older", Kind: "conversation_turn", Priority: 80, EstimatedTokens: 5, Content: json.RawMessage(`{"content":"old"}`)},
		{Ref: "policy-v1", Kind: "policy", Priority: 100, EstimatedTokens: 4, Content: json.RawMessage(`{"revision":"v1"}`)},
		{Ref: "turn-latest", Kind: "conversation_turn", Priority: 90, EstimatedTokens: 5, Content: json.RawMessage(`{"content":"latest"}`)},
	}}
	ids := contextIDs(0)
	pack, err := (ContextCompiler{Repository: repository, IDs: &ids, Budget: 9}).Compile(
		context.Background(),
		RunClaim{Conversation: domain.Conversation{ID: "conversation-1"}, Run: domain.AssistantRun{ID: "run-1", PolicyRevision: "assistant-readiness-v1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	selected := []string{pack.Selected[0].Ref, pack.Selected[1].Ref}
	if !reflect.DeepEqual(selected, []string{"policy-v1", "turn-latest"}) ||
		pack.PolicyRevision != "assistant-readiness-v1" ||
		len(pack.Dropped) != 1 || pack.Dropped[0].Ref != "turn-older" ||
		pack.Dropped[0].Reason != "context_budget_exceeded" || pack.EstimatedTokens != 9 ||
		len(repository.saved) != 1 {
		t.Fatalf("unexpected ContextPack: %+v", pack)
	}
}

func TestContextCompilerExcludesWorkspaceProtectedItemsUntilResolution(t *testing.T) {
	repository := &contextRepository{candidates: []ContextItem{
		{Ref: "turn-1", Kind: "conversation_turn", Priority: 100, EstimatedTokens: 3, Content: json.RawMessage(`{"content":"hello"}`)},
		{Ref: "repo-1", Kind: "repository", Priority: 90, EstimatedTokens: 3, Content: json.RawMessage(`{"name":"private"}`)},
		{Ref: "truth-1", Kind: "truth", Priority: 80, EstimatedTokens: 3, Content: json.RawMessage(`{"text":"private"}`)},
	}}
	ids := contextIDs(0)
	pack, err := (ContextCompiler{Repository: repository, IDs: &ids, Budget: 100}).Compile(
		context.Background(),
		RunClaim{Conversation: domain.Conversation{ID: "conversation-1"}, Run: domain.AssistantRun{ID: "run-1", PolicyRevision: "assistant-readiness-v1"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(pack.Selected) != 1 || len(pack.Dropped) != 2 {
		t.Fatalf("protected context leaked: %+v", pack)
	}
	for _, dropped := range pack.Dropped {
		if dropped.Reason != "workspace_unresolved" {
			t.Fatalf("unexpected drop reason: %+v", dropped)
		}
	}
}

func TestContextCompilerRequiresServerOwnedPolicyRevision(t *testing.T) {
	repository := &contextRepository{}
	ids := contextIDs(0)
	_, err := (ContextCompiler{Repository: repository, IDs: &ids, Budget: 100}).Compile(
		context.Background(),
		RunClaim{Conversation: domain.Conversation{ID: "conversation-1"}, Run: domain.AssistantRun{ID: "run-1"}},
	)
	if err == nil {
		t.Fatal("ContextCompiler accepted a Run without its server-owned policy revision")
	}
	if len(repository.saved) != 0 {
		t.Fatalf("invalid ContextPack was persisted: %+v", repository.saved)
	}
}

func TestContextPackDigestIsStableAcrossCandidateInputOrder(t *testing.T) {
	first := []ContextItem{
		{Ref: "b", Kind: "policy", Priority: 10, EstimatedTokens: 1, Content: json.RawMessage(`{"b":1}`)},
		{Ref: "a", Kind: "policy", Priority: 10, EstimatedTokens: 1, Content: json.RawMessage(`{"a":1}`)},
	}
	second := []ContextItem{first[1], first[0]}
	compile := func(candidates []ContextItem) ContextPack {
		repository := &contextRepository{candidates: candidates}
		ids := contextIDs(0)
		pack, err := (ContextCompiler{Repository: repository, IDs: &ids, Budget: 10}).Compile(
			context.Background(),
			RunClaim{Conversation: domain.Conversation{ID: "conversation-1"}, Run: domain.AssistantRun{ID: "run-1", PolicyRevision: "assistant-readiness-v1"}},
		)
		if err != nil {
			t.Fatal(err)
		}
		return pack
	}
	if left, right := compile(first), compile(second); left.Digest != right.Digest {
		t.Fatalf("ContextPack digest depends on input order: %s != %s", left.Digest, right.Digest)
	}
}
