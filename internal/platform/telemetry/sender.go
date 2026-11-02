package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const QueueCapacity = 1024
const BatchMaximum = 64

// LossReason is a finite sender loss classification.
type LossReason string

const (
	LossInvalid         LossReason = "invalid"
	LossQueueFull       LossReason = "queue_full"
	LossScanFailed      LossReason = "scan_failed"
	LossRemoteRejected  LossReason = "remote_rejected"
	LossTransportFailed LossReason = "transport_failed"
	LossShutdown        LossReason = "shutdown"
)

// SenderOptions selects the exact HTTP(S) push endpoint and required known-token scanner.
type SenderOptions struct {
	PushURL  string
	ScanJSON func(context.Context, string, any) error
}

// Sender queues safe events without blocking callers on scanning or network I/O.
type Sender struct {
	options     SenderOptions
	metrics     *Metrics
	queueEvents chan Event
	client      *http.Client
	mu          sync.Mutex
	started     bool
	stopped     bool
	lossPending uint64
}

// NewSender validates the endpoint and scanner without interpolating input into errors.
func NewSender(options SenderOptions, metrics *Metrics) (*Sender, error) {
	u, err := url.Parse(options.PushURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || options.ScanJSON == nil || metrics == nil {
		return nil, errors.New("invalid telemetry sender options")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Sender{options: options, metrics: metrics, queueEvents: make(chan Event, QueueCapacity), client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Enqueue performs bounded local validation and a non-blocking queue offer.
func (s *Sender) Enqueue(e Event) bool {
	e = ownEventNumbers(e)
	if !e.valid() {
		s.loss(LossInvalid, 1)
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		s.lossLocked(LossShutdown, 1)
		return false
	}
	select {
	case s.queueEvents <- e:
		s.metrics.queue(len(s.queueEvents))
		return true
	default:
		s.lossLocked(LossQueueFull, 1)
		return false
	}
}

func ownEventNumbers(e Event) Event {
	if e.DurationMillis != nil {
		value := *e.DurationMillis
		e.DurationMillis = &value
	}
	if e.Bytes != nil {
		value := *e.Bytes
		e.Bytes = &value
	}
	if e.Count != nil {
		value := *e.Count
		e.Count = &value
	}
	return e
}

func (s *Sender) loss(r LossReason, n uint64) { s.mu.Lock(); s.lossLocked(r, n); s.mu.Unlock() }
func (s *Sender) lossLocked(r LossReason, n uint64) {
	s.lossPending += n
	s.metrics.dropped(r, n, time.Now())
}

// Run owns background scanning and HTTP sending. A second Run is a no-op.
func (s *Sender) Run(ctx context.Context) {
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	defer s.shutdown()
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case first := <-s.queueEvents:
			batch := []Event{first}
			for len(batch) < BatchMaximum-1 {
				select {
				case e := <-s.queueEvents:
					batch = append(batch, e)
				default:
					goto ready
				}
			}
		ready:
			s.mu.Lock()
			s.metrics.queue(len(s.queueEvents))
			s.mu.Unlock()
			if ctx.Err() != nil {
				s.loss(LossShutdown, uint64(len(batch)))
				return
			}
			s.send(ctx, batch)
		}
	}
}

func (s *Sender) shutdown() {
	s.mu.Lock()
	s.stopped = true
	for {
		select {
		case <-s.queueEvents:
			s.lossLocked(LossShutdown, 1)
		default:
			s.metrics.queue(0)
			s.mu.Unlock()
			return
		}
	}
}

func (s *Sender) send(parent context.Context, batch []Event) {
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	originalCount := len(batch)
	s.mu.Lock()
	pending := s.lossPending
	s.mu.Unlock()
	gapIncluded := pending > 0
	if gapIncluded {
		n := int64(pending)
		if pending > uint64(1<<63-1) {
			n = 1<<63 - 1
		}
		batch = append(batch, Event{TS: time.Now().UTC(), Level: LevelWarn, Service: Service, Component: ComponentTelemetry, Event: EventLogGap, SchemaVersion: SchemaVersion, Count: &n})
	}
	type lokiStream struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	streams := make(map[string]*lokiStream)
	for _, e := range batch {
		b, err := e.marshaled()
		if err != nil {
			s.loss(LossInvalid, uint64(originalCount))
			return
		}
		key := string(e.Component) + "/" + string(e.Level)
		stream := streams[key]
		if stream == nil {
			stream = &lokiStream{Stream: map[string]string{"service": Service, "environment": Environment, "component": string(e.Component), "level": string(e.Level)}}
			streams[key] = stream
		}
		stream.Values = append(stream.Values, [2]string{strconv.FormatInt(e.TS.UnixNano(), 10), string(b)})
	}
	requestBody := struct {
		Streams []*lokiStream `json:"streams"`
	}{Streams: make([]*lokiStream, 0, len(streams))}
	for _, stream := range streams {
		requestBody.Streams = append(requestBody.Streams, stream)
	}
	if err := s.options.ScanJSON(ctx, "telemetry.loki.push", requestBody); err != nil {
		s.loss(LossScanFailed, uint64(originalCount))
		return
	}
	b, err := json.Marshal(requestBody)
	if err != nil {
		s.loss(LossInvalid, uint64(originalCount))
		return
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.options.PushURL, bytes.NewReader(b))
	if err != nil {
		s.loss(LossInvalid, uint64(originalCount))
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		if parent.Err() != nil {
			s.loss(LossShutdown, uint64(originalCount))
		} else {
			s.loss(LossTransportFailed, uint64(originalCount))
		}
		return
	}
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if readErr != nil {
		if parent.Err() != nil {
			s.loss(LossShutdown, uint64(originalCount))
		} else {
			s.loss(LossTransportFailed, uint64(originalCount))
		}
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.loss(LossRemoteRejected, uint64(originalCount))
		return
	}
	s.metrics.receiverAccepted(len(batch))
	if gapIncluded {
		s.mu.Lock()
		if s.lossPending >= pending {
			s.lossPending -= pending
		} else {
			s.lossPending = 0
		}
		s.mu.Unlock()
	}
}
