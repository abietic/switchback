package localproduct

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abietic/switchback/internal/platform/telemetry"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/jackc/pgx/v5/pgxpool"
)

// telemetryRunner is optional infrastructure. Neither construction nor scraping
// accesses PostgreSQL, a listener, a credential file or a remote receiver.
type telemetryRunner struct {
	configuration Config
	metrics       *telemetry.Metrics
	once          sync.Once
	sampleFailed  bool             // owned by the single sampling loop
	now           func() time.Time // Backend clock; set before Run, never concurrently.
}

func newTelemetryRunner(configuration Config) *telemetryRunner {
	return &telemetryRunner{configuration: configuration, metrics: telemetry.NewMetrics(), now: time.Now}
}

func (runner *telemetryRunner) Run(ctx context.Context) {
	first := false
	runner.once.Do(func() { first = true })
	if first {
		runner.run(ctx)
	}
}

func (runner *telemetryRunner) run(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	var workers sync.WaitGroup
	runCtx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); workers.Wait() }()

	var server *http.Server
	if address := runner.configuration.TelemetryAddress; address != "" {
		host, port, err := net.SplitHostPort(address)
		number, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || number < 1 || number > 65535 {
			telemetryDiagnostic("telemetry_address_invalid")
		} else if listener, err := telemetryListen(ctx, address); err != nil {
			telemetryDiagnostic("telemetry_bind_failed")
		} else {
			mux := http.NewServeMux()
			mux.Handle("GET /metrics", runner.metrics.Handler())
			server = &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
			workers.Add(1)
			go func() {
				defer workers.Done()
				if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
					telemetryDiagnostic("telemetry_listener_stopped")
				}
			}()
			defer func() {
				shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
				defer stop()
				_ = server.Shutdown(shutdownCtx)
				_ = server.Close()
			}()
		}
	}

	var sender *telemetry.Sender
	if runner.configuration.AlloyPushURL != "" {
		if !validTelemetryPushURL(runner.configuration.AlloyPushURL) {
			telemetryDiagnostic("telemetry_sender_invalid")
		} else if scanner, err := newRuntimeKnownTokenScanner(runner.configuration); err != nil {
			telemetryDiagnostic("telemetry_scanner_unavailable")
		} else {
			sender, err = telemetry.NewSender(telemetry.SenderOptions{PushURL: runner.configuration.AlloyPushURL, ScanJSON: scanner.ScanJSON}, runner.metrics)
			if err != nil {
				telemetryDiagnostic("telemetry_sender_invalid")
			}
		}
	}
	if sender != nil {
		workers.Add(1)
		go func() { defer workers.Done(); sender.Run(runCtx) }()
		telemetryEvent(sender, telemetry.EventBackendStarted, telemetry.CodeOK)
		// Shutdown is best effort; the sender accounts for any discarded record.
		defer telemetryEvent(sender, telemetry.EventBackendStopped, telemetry.CodeOK)
	}
	if server == nil && sender == nil {
		return
	}
	workers.Add(1)
	go func() { defer workers.Done(); runner.sampleLoop(runCtx, sender) }()
	<-ctx.Done()
}

func telemetryListen(ctx context.Context, address string) (net.Listener, error) {
	ctx, cancel := context.WithTimeout(ctx, telemetrySampleTimeout)
	defer cancel()
	return (&net.ListenConfig{}).Listen(ctx, "tcp", address)
}

func validTelemetryPushURL(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u == nil || strings.TrimSpace(endpoint) != endpoint ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		return err == nil && number >= 1 && number <= 65535
	}
	return !strings.HasSuffix(u.Host, ":")
}

func (runner *telemetryRunner) sampleLoop(ctx context.Context, sender *telemetry.Sender) {
	configuration, err := pgxpool.ParseConfig(runner.configuration.DatabaseURL)
	if err != nil {
		runner.metrics.SampleFailed()
		telemetryDiagnostic("telemetry_database_config_invalid")
		telemetryEvent(sender, telemetry.EventSampleFailed, telemetry.CodeFailed)
		return
	}
	configuration.MaxConns = 2
	configuration.MinConns = 0
	configuration.MinIdleConns = 0
	configuration.ConnConfig.ConnectTimeout = telemetrySampleTimeout
	configuration.ConnConfig.RuntimeParams["statement_timeout"] = "2000"
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	if err != nil {
		runner.metrics.SampleFailed()
		telemetryDiagnostic("telemetry_database_unavailable")
		return
	}
	defer pool.Close()
	// All Worker observation I/O shares this optional two-connection pool; no
	// collector readiness or network operation is introduced into execution.
	workerCtx, stopWorkers := context.WithCancel(ctx)
	var workerObservers sync.WaitGroup
	defer func() { stopWorkers(); workerObservers.Wait() }()
	if sender != nil {
		emit := func(e telemetry.Event) { sender.Enqueue(e) }
		terminals := newWorkerTerminalObserver(pool, runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID, runner.metrics, emit, runner.now)
		workerObservers.Add(1)
		go func() { defer workerObservers.Done(); terminals.run(workerCtx) }()
		if engine, err := dockeradapter.NewClient(); err != nil {
			runner.metrics.WorkerCollectorLost(telemetry.WorkerUnavailable)
		} else {
			workerObservers.Add(1)
			go func() {
				defer workerObservers.Done()
				defer engine.Close()
				collector := workerOutputCollector{discover: func(ctx context.Context) ([]workerOutputAuthority, error) {
					return discoverWorkerOutput(ctx, pool, runner.configuration.Profile.TenantID, runner.configuration.Profile.ActorUserID)
				}, docker: workerOutputEngine{engine}, emit: emit, metrics: runner.metrics}
				collector.run(workerCtx)
			}()
		}
	}
	runner.sample(ctx, pool, sender)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runner.sample(ctx, pool, sender)
		}
	}
}

func (runner *telemetryRunner) sample(ctx context.Context, pool *pgxpool.Pool, sender *telemetry.Sender) {
	snapshot, err := (telemetrySampler{pool: pool, tenantID: runner.configuration.Profile.TenantID, userID: runner.configuration.Profile.ActorUserID}).Sample(ctx)
	if err == nil {
		err = runner.metrics.Replace(snapshot)
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		runner.metrics.SampleFailed()
		if !runner.sampleFailed {
			telemetryDiagnostic("telemetry_sample_failed")
			telemetryEvent(sender, telemetry.EventSampleFailed, telemetry.CodeFailed)
		}
		runner.sampleFailed = true
		return
	}
	if runner.sampleFailed {
		telemetryEvent(sender, telemetry.EventSampleRecovered, telemetry.CodeOK)
	}
	runner.sampleFailed = false
}

func telemetryDiagnostic(code string) {
	slog.Warn("local telemetry disabled or degraded", "code", code)
}

func telemetryEvent(sender *telemetry.Sender, kind telemetry.EventKind, code telemetry.Code) {
	if sender == nil {
		return
	}
	level := telemetry.LevelInfo
	component := telemetry.ComponentBackend
	if kind == telemetry.EventSampleFailed {
		level = telemetry.LevelWarn
	}
	if kind == telemetry.EventSampleFailed || kind == telemetry.EventSampleRecovered {
		component = telemetry.ComponentTelemetry
	}
	sender.Enqueue(telemetry.Event{TS: time.Now(), Service: telemetry.Service, SchemaVersion: telemetry.SchemaVersion, Level: level, Component: component, Event: kind, Code: code})
}
