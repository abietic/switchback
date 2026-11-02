package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type modelCapturePreparer struct{ capture RuntimeObservationCapture }

func (p modelCapturePreparer) PrepareModelObservation(context.Context, WorkerControlEvent) RuntimeObservationCapture {
	return p.capture
}

func TestModelObservationCaptureIsBackendOnly(t *testing.T) {
	c := RuntimeObservationCapture{Sealed: []byte("must-not-reach-worker"), SafeCode: ObservationCaptureUnavailable}
	raw, err := json.Marshal(ApplyRuntimeControlEventCommand{ObservationCapture: &c})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ObservationCapture") || strings.Contains(string(raw), "Sealed") || strings.Contains(string(raw), "observation_capture_unavailable") {
		t.Fatal("capture escaped Backend-only API")
	}
	raw, err = json.Marshal(c)
	if err != nil || string(raw) != "{}" {
		t.Fatalf("capture wire=%s err=%v", raw, err)
	}
}

func TestModelObservationAcceptorRequiresDedicatedReceiver(t *testing.T) {
	a := ModelObservationAcceptor{Preparer: modelCapturePreparer{}}
	if err := a.Accept(context.Background(), WorkerControlEvent{}); err == nil {
		t.Fatal("accepted wrong event type")
	}
	e := WorkerControlEvent{}
	e.Envelope.MessageType = "model.observation"
	if err := a.Accept(context.Background(), e); err == nil {
		t.Fatal("accepted missing dedicated delivery")
	}
	e.Delivery = &modelCaptureDelivery{}
	if err := (ModelObservationAcceptor{}).Accept(context.Background(), e); err == nil {
		t.Fatal("accepted missing preparer")
	}
}

type modelCaptureDelivery struct {
	capture RuntimeObservationCapture
	outcome string
}

func (*modelCaptureDelivery) ReleaseForRetry() {}

func (*modelCaptureDelivery) Apply(context.Context, json.RawMessage) error {
	panic("ordinary Apply bypass")
}
func (d *modelCaptureDelivery) ApplyModelObservation(_ context.Context, outcome json.RawMessage, c RuntimeObservationCapture) error {
	d.capture = c
	d.outcome = string(outcome)
	return nil
}
func TestModelObservationAcceptorCopiesCaptureAndRecordsSafeGap(t *testing.T) {
	for _, capture := range []RuntimeObservationCapture{{Sealed: []byte("sealed")}, {SafeCode: ObservationCaptureUnavailable}} {
		d := &modelCaptureDelivery{}
		event := WorkerControlEvent{Delivery: d}
		event.Envelope.MessageType = "model.observation"
		if err := (ModelObservationAcceptor{Preparer: modelCapturePreparer{capture}}).Accept(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if d.outcome != `{"status":"model_observation_recorded"}` {
			t.Fatal(d.outcome)
		}
		if len(capture.Sealed) > 0 {
			capture.Sealed[0] = 'x'
			if string(d.capture.Sealed) != "sealed" {
				t.Fatal("capture aliases caller")
			}
		} else if d.capture.SafeCode != ObservationCaptureUnavailable {
			t.Fatal("lost safe gap")
		}
	}
}
