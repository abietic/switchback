package trace

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func TestJSONLRecorderSerializesConcurrentEvents(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	recorder, err := NewJSONLRecorder(&output)
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	const eventCount = 32
	var wait sync.WaitGroup
	for index := range eventCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if recordErr := recorder.Record(Event{
				Type:      TypeRawFrame,
				Direction: "client_to_agent",
				Frame:     fmt.Sprintf("{\"id\":%d}\n", index),
			}); recordErr != nil {
				t.Errorf("record event %d: %v", index, recordErr)
			}
		}()
	}
	wait.Wait()

	seen := make(map[string]struct{}, eventCount)
	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode JSONL record: %v", err)
		}
		if event.Type != TypeRawFrame || event.Direction != "client_to_agent" {
			t.Fatalf("event = %+v", event)
		}
		seen[event.Frame] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan JSONL: %v", err)
	}
	if len(seen) != eventCount {
		t.Fatalf("unique event count = %d, want %d", len(seen), eventCount)
	}
}

func TestJSONLRecorderRejectsInvalidConstructionAndEvent(t *testing.T) {
	t.Parallel()

	if _, err := NewJSONLRecorder(nil); err == nil {
		t.Fatal("nil destination unexpectedly succeeded")
	}
	recorder, err := NewJSONLRecorder(&bytes.Buffer{})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	if err := recorder.Record(Event{}); err == nil {
		t.Fatal("empty event type unexpectedly succeeded")
	}
}
