package application_test

import (
	"encoding/json"
	"errors"
	"testing"

	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

const observationSourceDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestTurnSettlementCheckpointProofAcceptsLegacyAndCompleteObservationSource(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		checkpoint := validTurnSettlementCheckpointForPhase(t, workerapp.TurnSettlementEvidencePrepared)
		if err := workerapp.ValidateTurnSettlementCheckpointProof(workerapp.TurnSettlementEvidencePrepared, checkpoint); err != nil {
			t.Fatalf("legacy checkpoint rejected: %v", err)
		}
	})

	t.Run("complete source", func(t *testing.T) {
		checkpoint := observationSourceCheckpoint(t)
		if err := workerapp.ValidateTurnSettlementCheckpointProof(workerapp.TurnSettlementEvidencePrepared, checkpoint); err != nil {
			t.Fatalf("source checkpoint rejected: %v", err)
		}
	})
}

func TestTurnSettlementCheckpointProofRejectsInvalidObservationSource(t *testing.T) {
	tests := map[string]func(map[string]any){
		"partial triple": func(root map[string]any) {
			delete(root["evidence_input"].(map[string]any), "observation_source_digest")
		},
		"unsafe path": func(root map[string]any) {
			root["evidence_input"].(map[string]any)["observation_source_snapshot_file"] = "../source.jsonl"
		},
		"nul in basename": func(root map[string]any) {
			root["evidence_input"].(map[string]any)["observation_source_snapshot_file"] = "source\x00.jsonl"
		},
		"zero size": func(root map[string]any) {
			root["evidence_input"].(map[string]any)["observation_source_size"] = float64(0)
		},
		"noncanonical digest": func(root map[string]any) {
			root["evidence_input"].(map[string]any)["observation_source_digest"] = "SHA256:BBBB"
		},
		"object digest tamper": func(root map[string]any) {
			observationObject(root)["Stored"].(map[string]any)["Checksum"] = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		},
		"object size tamper": func(root map[string]any) {
			observationObject(root)["Stored"].(map[string]any)["Size"] = float64(18)
		},
		"segment uri tamper": func(root map[string]any) {
			observationSegment(root)["uri"] = "object://objects/other-source"
		},
		"segment digest tamper": func(root map[string]any) {
			observationSegment(root)["sha256"] = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		},
		"segment size tamper": func(root map[string]any) {
			observationSegment(root)["size_bytes"] = float64(18)
		},
		"missing source object": func(root map[string]any) {
			objects := root["evidence"].(map[string]any)["finalized"].(map[string]any)["Objects"].([]any)
			root["evidence"].(map[string]any)["finalized"].(map[string]any)["Objects"] = objects[:4]
		},
		"duplicate source object": func(root map[string]any) {
			finalized := root["evidence"].(map[string]any)["finalized"].(map[string]any)
			objects := finalized["Objects"].([]any)
			finalized["Objects"] = append(objects, objects[4])
		},
		"duplicate source object replaces required raw object": func(root map[string]any) {
			objects := root["evidence"].(map[string]any)["finalized"].(map[string]any)["Objects"].([]any)
			objects[0] = objects[4]
		},
		"unknown object": func(root map[string]any) {
			observationObject(root)["Kind"] = "turn-observation-unknown"
		},
		"missing source segment": func(root map[string]any) {
			manifest := observationManifest(root)
			segments := manifest["segments"].([]any)
			manifest["segments"] = segments[:2]
		},
		"duplicate source segment": func(root map[string]any) {
			manifest := observationManifest(root)
			segments := manifest["segments"].([]any)
			manifest["segments"] = append(segments, segments[2])
		},
		"duplicate source segment replaces required raw segment": func(root map[string]any) {
			segments := observationManifest(root)["segments"].([]any)
			segments[0] = segments[2]
		},
		"unknown segment": func(root map[string]any) {
			observationSegment(root)["kind"] = "turn-observation-unknown"
		},
		"source proof absent": func(root map[string]any) {
			input := root["evidence_input"].(map[string]any)
			delete(input, "observation_source_snapshot_file")
			delete(input, "observation_source_size")
			delete(input, "observation_source_digest")
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := observationSourceCheckpointMap(t)
			mutate(root)
			checkpoint, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			err = workerapp.ValidateTurnSettlementCheckpointProof(workerapp.TurnSettlementEvidencePrepared, checkpoint)
			if !errors.Is(err, workerapp.ErrTurnSettlementCheckpointConflict) {
				t.Fatalf("invalid observation source proof error=%v", err)
			}
		})
	}
}

func observationSourceCheckpoint(t *testing.T) json.RawMessage {
	t.Helper()
	root := observationSourceCheckpointMap(t)
	checkpoint, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func observationSourceCheckpointMap(t *testing.T) map[string]any {
	t.Helper()
	checkpoint := validTurnSettlementCheckpointForPhase(t, workerapp.TurnSettlementEvidencePrepared)
	var root map[string]any
	if err := json.Unmarshal(checkpoint, &root); err != nil {
		t.Fatal(err)
	}
	input := root["evidence_input"].(map[string]any)
	input["observation_source_snapshot_file"] = "observation-source-bbbb.jsonl"
	input["observation_source_size"] = float64(17)
	input["observation_source_digest"] = observationSourceDigest

	finalized := root["evidence"].(map[string]any)["finalized"].(map[string]any)
	objects := finalized["Objects"].([]any)
	finalized["Objects"] = append(objects, map[string]any{
		"Kind": "turn-observation-source",
		"Stored": map[string]any{
			"Key":      "objects/observation-source",
			"Size":     float64(17),
			"Checksum": observationSourceDigest,
		},
	})
	manifest := finalized["Manifest"].(map[string]any)
	segments := manifest["segments"].([]any)
	manifest["segments"] = append(segments, map[string]any{
		"kind":           "turn-observation-source",
		"uri":            "object://objects/observation-source",
		"first_sequence": float64(1),
		"last_sequence":  float64(1),
		"sha256":         observationSourceDigest[len("sha256:"):],
		"size_bytes":     float64(17),
		"record_count":   float64(1),
	})
	return root
}

func observationObject(root map[string]any) map[string]any {
	objects := root["evidence"].(map[string]any)["finalized"].(map[string]any)["Objects"].([]any)
	return objects[4].(map[string]any)
}

func observationManifest(root map[string]any) map[string]any {
	return root["evidence"].(map[string]any)["finalized"].(map[string]any)["Manifest"].(map[string]any)
}

func observationSegment(root map[string]any) map[string]any {
	return observationManifest(root)["segments"].([]any)[2].(map[string]any)
}
