// Command realprovider drives the production Worker ports against real model providers.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	contractsv1 "github.com/abietic/switchback/internal/contracts/v1"
	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/platform/secretcheck"
	"github.com/abietic/switchback/internal/testsupport/failurematrix"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	dockeradapter "github.com/abietic/switchback/internal/workerruntime/adapter/docker"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	"github.com/abietic/switchback/internal/workerruntime/application"
	"github.com/moby/moby/client"
	"google.golang.org/grpc"
)

const (
	grpcListenAddress = ":9090"
	turnTimeout       = 20 * time.Minute
	invalidCredential = "switchback-m1-invalid-provider-credential-v1"
	sourceFixturePath = "/opt/switchback-acceptance/fixture.txt"
)

var errAgentTurnFailed = errors.New("AgentTurn reported an error")

type options struct {
	dataRoot        string
	engineDataRoot  string
	workerImage     string
	network         string
	grpcTarget      string
	httpBaseURL     string
	kimiKeyPath     string
	deepSeekKeyPath string
	summaryPath     string
	mode            string
}

type acceptanceSummary struct {
	SchemaVersion             string           `json:"schema_version"`
	StartedAt                 time.Time        `json:"started_at"`
	CompletedAt               time.Time        `json:"completed_at"`
	Outcome                   string           `json:"outcome"`
	FailureVariant            string           `json:"failure_variant,omitempty"`
	SourceFixtureSHA256Before string           `json:"source_fixture_sha256_before"`
	SourceFixtureSHA256After  string           `json:"source_fixture_sha256_after"`
	SourceFixtureImmutable    bool             `json:"source_fixture_immutable"`
	KnownTokenAbsence         bool             `json:"known_token_absence"`
	Profiles                  []profileSummary `json:"profiles"`
}

type identityCardinality struct {
	WorkerSessions int `json:"worker_sessions"`
	WorkerRuns     int `json:"worker_runs"`
	Runtimes       int `json:"runtimes"`
	AgentSessions  int `json:"agent_sessions"`
	AgentTurns     int `json:"agent_turns"`
}

type profileSummary struct {
	ProfileID                    string              `json:"profile_id"`
	ProfileRevision              uint64              `json:"profile_revision"`
	ProfileDigest                string              `json:"profile_digest"`
	UpstreamProtocol             string              `json:"upstream_protocol"`
	WorkerSessionID              string              `json:"worker_session_id"`
	WorkerRunID                  string              `json:"worker_run_id"`
	RuntimeID                    string              `json:"runtime_instance_id"`
	RuntimeGeneration            uint64              `json:"runtime_generation"`
	ContainerID                  string              `json:"container_id"`
	AgentSessionID               string              `json:"agent_session_id"`
	AgentTurnIDs                 []string            `json:"agent_turn_ids"`
	TerminalReasons              []string            `json:"terminal_reasons"`
	ValidationMode               string              `json:"validation_mode"`
	ResponseAssertions           []string            `json:"response_assertions,omitempty"`
	TraceSHA256                  string              `json:"trace_sha256"`
	CleanupConfirmed             bool                `json:"cleanup_confirmed"`
	ExecutionOutcome             string              `json:"execution_outcome"`
	AcceptanceOutcome            string              `json:"acceptance_outcome"`
	WorkspaceFixtureSHA256Before string              `json:"workspace_fixture_sha256_before"`
	WorkspaceFixtureSHA256After  string              `json:"workspace_fixture_sha256_after"`
	FixtureImmutable             bool                `json:"fixture_immutable"`
	KnownTokenAbsence            bool                `json:"known_token_absence"`
	IdentityCardinality          identityCardinality `json:"identity_cardinality"`
	IdentityCardinalityVerified  bool                `json:"identity_cardinality_verified"`
	ManagedContainerResidue      int                 `json:"managed_container_residue"`
	ManagedSecretVolumeResidue   int                 `json:"managed_secret_volume_residue"`
	ManagedResidueVerified       bool                `json:"managed_residue_verified"`
	RecoveryAttempted            bool                `json:"recovery_attempted,omitempty"`
	RecoveryScenario             string              `json:"recovery_scenario,omitempty"`
	ProductionRecoveryPath       *bool               `json:"production_recovery_path,omitempty"`
	ReplacementRuntimeID         string              `json:"replacement_runtime_instance_id,omitempty"`
	ReplacementRuntimeGeneration uint64              `json:"replacement_runtime_generation,omitempty"`
	ReplacementContainerID       string              `json:"replacement_container_id,omitempty"`
	AgentSessionPreserved        bool                `json:"agent_session_preserved,omitempty"`
	NoPromptReplay               bool                `json:"no_prompt_replay,omitempty"`
	runtimeIDs                   []string
}

func main() {
	if err := run(context.Background(), os.LookupEnv); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		if cause := rootCause(err); cause != err {
			_, _ = fmt.Fprintln(os.Stderr, "acceptance root cause:", cause)
		}
		os.Exit(1)
	}
}

func rootCause(err error) error {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}

func run(ctx context.Context, lookupEnv func(string) (string, bool)) error {
	opts, err := loadOptions(lookupEnv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.dataRoot, 0o700); err != nil {
		return fmt.Errorf("prepare acceptance data root")
	}
	sourceFixtureBefore, err := checksumFile(sourceFixturePath)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", grpcListenAddress)
	if err != nil {
		return fmt.Errorf("listen for WorkerControl")
	}
	defer listener.Close()
	hub := controlgrpc.NewHub()
	server := grpc.NewServer()
	controlgrpc.RegisterWorkerControlRPCServer(server, controlgrpc.NewServer(hub))
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	defer server.GracefulStop()

	engine, err := dockeradapter.NewClient()
	if err != nil {
		return fmt.Errorf("connect Docker Engine")
	}
	defer engine.Close()
	provisioner, err := dockeradapter.NewProvisioner(engine, dockeradapter.Options{
		Image: opts.workerImage, Network: opts.network, EngineDataRoot: opts.engineDataRoot,
		BackendGRPCTarget: opts.grpcTarget, BackendHTTPBaseURL: opts.httpBaseURL,
		HostPathVerifier: dockeradapter.LocalHostPathVerifier{},
	})
	if err != nil {
		return err
	}
	summary := acceptanceSummary{
		SchemaVersion: "switchback.real-provider-acceptance.v1", StartedAt: time.Now().UTC(), Outcome: "failed",
		SourceFixtureSHA256Before: sourceFixtureBefore,
	}
	defer func() {
		summary.CompletedAt = time.Now().UTC()
		_ = writeSummary(opts.summaryPath, summary)
	}()

	registry := config.ProductionProfileRegistry()
	knownTokens := make([]secretcheck.KnownToken, 0, 16)
	profiles := []struct {
		id      string
		keyPath string
	}{
		{id: config.KimiCodexAnthropicV1, keyPath: opts.kimiKeyPath},
		{id: config.DeepSeekCodexOpenAIV1, keyPath: opts.deepSeekKeyPath},
	}
	if opts.mode == "invalid-credential" {
		profiles = profiles[:1]
		summary.FailureVariant = "invalid-credential"
	} else if opts.mode == "recovery" {
		profiles = profiles[1:]
	}
	for index, selected := range profiles {
		profile, ok := registry.Agent(selected.id)
		if !ok {
			return fmt.Errorf("selected AgentProfile is unavailable")
		}
		tokenStart := len(knownTokens)
		result, err := runProfile(ctx, index+1, opts, hub, provisioner, profile, selected.keyPath,
			opts.mode == "invalid-credential", &knownTokens)
		if err == nil {
			result.ManagedContainerResidue, result.ManagedSecretVolumeResidue, err = managedResidue(
				ctx,
				engine,
				result.runtimeIDs,
			)
			result.ManagedResidueVerified = err == nil && result.ManagedContainerResidue == 0 && result.ManagedSecretVolumeResidue == 0
			if err == nil && !result.ManagedResidueVerified {
				err = errors.New("managed Runtime residue remains")
			}
		}
		if err == nil {
			var scanner *secretcheck.Scanner
			scanner, err = secretcheck.New(knownTokens[tokenStart:])
			if err == nil {
				err = scanPersistedTree(ctx, scanner, filepath.Join(opts.dataRoot, "sessions", result.WorkerSessionID))
			}
			result.KnownTokenAbsence = err == nil
		}
		summary.Profiles = append(summary.Profiles, result)
		if err != nil {
			return fmt.Errorf("profile %s acceptance failed: %w", profile.ID, err)
		}
	}
	summary.SourceFixtureSHA256After, err = checksumFile(sourceFixturePath)
	if err != nil {
		return err
	}
	summary.SourceFixtureImmutable = summary.SourceFixtureSHA256Before == summary.SourceFixtureSHA256After
	if !summary.SourceFixtureImmutable {
		return errors.New("source acceptance fixture changed")
	}
	knownTokenScanner, err := secretcheck.New(knownTokens)
	if err != nil {
		return err
	}
	if err := knownTokenScanner.ScanJSON(ctx, "acceptance.summary", summary); err != nil {
		return err
	}
	summary.KnownTokenAbsence = true
	if opts.mode == "invalid-credential" {
		result := failurematrix.Result{
			Variant: "invalid-credential", ExecutionOutcome: "failed", AcceptanceOutcome: "pending",
			Assertions: []string{"model_authentication_rejected", "false_completion_absent", "runtime_cleanup_confirmed"},
		}
		if directory := os.Getenv(failurematrix.ResultDirEnv); directory != "" {
			if err := failurematrix.WriteResult(directory, result); err != nil {
				return err
			}
		}
	}
	select {
	case err := <-serveErrors:
		if err != nil {
			return fmt.Errorf("WorkerControl server failed")
		}
	default:
	}
	summary.Outcome = "passed"
	summary.CompletedAt = time.Now().UTC()
	if err := writeSummary(opts.summaryPath, summary); err != nil {
		return err
	}
	encoded, _ := json.Marshal(map[string]string{"outcome": summary.Outcome, "summary": opts.summaryPath})
	_, _ = fmt.Fprintln(os.Stdout, string(encoded))
	return nil
}

func runProfile(
	ctx context.Context,
	ordinal int,
	opts options,
	hub *controlgrpc.Hub,
	provisioner *dockeradapter.Provisioner,
	profile config.AgentProfile,
	keyPath string,
	expectInvalidCredential bool,
	knownTokens *[]secretcheck.KnownToken,
) (result profileSummary, resultErr error) {
	short := "kimi"
	if profile.ID == config.DeepSeekCodexOpenAIV1 {
		short = "deepseek"
	}
	if expectInvalidCredential {
		short += "-invalid"
	}
	sessionID := fmt.Sprintf("acceptance-%s-session-%d", short, ordinal)
	runID := fmt.Sprintf("acceptance-%s-run-%d", short, ordinal)
	runtimeID := fmt.Sprintf("acceptance-%s-runtime-%d", short, ordinal)
	result = profileSummary{
		ProfileID: profile.ID, ProfileRevision: profile.Revision, ProfileDigest: profile.Digest,
		UpstreamProtocol: profile.UpstreamProtocol, WorkerSessionID: sessionID, WorkerRunID: runID,
		RuntimeID: runtimeID, RuntimeGeneration: 1,
		runtimeIDs: []string{runtimeID},
	}
	credential, err := controlgrpc.NewCredential(controlgrpc.RuntimeIdentity{
		WorkerSessionID: sessionID, RuntimeInstanceID: runtimeID, RuntimeGeneration: 1, ChannelEpoch: 1,
	})
	if err != nil {
		return result, err
	}
	if err := hub.RegisterCredential(credential); err != nil {
		return result, err
	}
	defer hub.Revoke(credential.Identity)
	events, err := hub.Events(ctx, runtimeID, 1)
	if err != nil {
		return result, err
	}
	storage, err := (localstorage.Materializer{
		BackendDataRoot: opts.dataRoot, EngineDataRoot: opts.engineDataRoot, Ownership: localstorage.OSOwnership{},
	}).Materialize(sessionID)
	if err != nil {
		return result, err
	}
	if err := writeFixture(storage.WorkingCopy.BackendPath); err != nil {
		return result, err
	}
	result.WorkspaceFixtureSHA256Before, err = checksumFile(filepath.Join(storage.WorkingCopy.BackendPath, "fixture.txt"))
	if err != nil {
		return result, err
	}
	var providerCredential runtimesecret.ProviderCredential
	if expectInvalidCredential {
		providerCredential = runtimesecret.ProviderCredential([]byte(invalidCredential))
	} else {
		providerCredential, err = readCredential(keyPath)
		if err != nil {
			return result, err
		}
	}
	defer zero(providerCredential)
	secretResources, err := (runtimesecret.Materializer{
		Ownership: runtimesecret.OSOwnership{}, OwnerUID: runtimesecret.OSOwnerUID,
	}).Materialize(runtimesecret.Request{
		WorkerSessionID: sessionID, WorkerRunID: runID, RuntimeInstanceID: runtimeID,
		Generation: 1, ChannelEpoch: 1, WorkerControlToken: credential.Token, SessionStorage: storage,
		AgentProfile:       application.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest},
		ProviderCredential: providerCredential,
	})
	if err != nil {
		return result, err
	}
	bootstrapBytes, err := os.ReadFile(secretResources.Bootstrap.BackendPath)
	if err != nil {
		return result, fmt.Errorf("read Runtime bootstrap for acceptance registry")
	}
	var bootstrap runtimesecret.Bootstrap
	if err := json.Unmarshal(bootstrapBytes, &bootstrap); err != nil {
		return result, fmt.Errorf("decode Runtime bootstrap for acceptance registry")
	}
	labelPrefix := fmt.Sprintf("profile-%d-", ordinal)
	*knownTokens = append(*knownTokens,
		secretcheck.KnownToken{Label: labelPrefix + "provider", Value: string(providerCredential)},
		secretcheck.KnownToken{Label: labelPrefix + "control", Value: credential.Token},
		secretcheck.KnownToken{Label: labelPrefix + "proxy", Value: bootstrap.ProxyToken},
		secretcheck.KnownToken{Label: labelPrefix + "model-sink", Value: bootstrap.ModelSinkToken},
	)
	resourceProfile, _ := config.ProductionProfileRegistry().RuntimeResource(config.LocalStandardV1)
	networkProfile, err := config.ProductionRuntimeNetworkProfiles().ResolveForDeployment(
		config.SingleUserLocalProfileName,
	)
	if err != nil {
		return result, err
	}
	networkEvidence := networkProfile.Evidence()
	runtime, err := provisioner.Provision(ctx, application.RuntimeProvisionRequest{
		WorkerSessionID: sessionID, WorkerRunID: runID, RuntimeInstanceID: runtimeID, Generation: 1,
		ResourceProfile: application.ProfileRef{ID: resourceProfile.ID, Revision: resourceProfile.Revision, Digest: resourceProfile.Digest},
		AgentProfile:    application.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest},
		NetworkProfile: application.ProfileRef{
			ID: networkProfile.ID, Revision: networkProfile.Revision, Digest: networkProfile.Digest,
		},
		NetworkEnforcementStatus: networkEvidence.EnforcementStatus,
		Storage:                  storage, Secrets: secretResources,
	})
	if err != nil {
		return result, err
	}
	result.ContainerID = runtime.ContainerID
	runtimeGenerations := map[string]uint64{runtimeID: 1}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result.CleanupConfirmed = true
		var cleanupErrors []error
		for _, managedRuntimeID := range result.runtimeIDs {
			observation, cleanupErr := provisioner.Cleanup(
				cleanupCtx,
				application.RuntimeCleanupRequest{
					RuntimeInstanceID: managedRuntimeID,
					Generation:        runtimeGenerations[managedRuntimeID],
				},
			)
			confirmed := cleanupErr == nil && observation.Confirmed
			result.CleanupConfirmed = result.CleanupConfirmed && confirmed
			if cleanupErr == nil && !observation.Confirmed {
				cleanupErr = errors.New("Runtime cleanup was not confirmed")
			}
			cleanupErrors = append(cleanupErrors, cleanupErr)
		}
		resultErr = errors.Join(append([]error{resultErr}, cleanupErrors...)...)
	}()

	ready, err := waitForMessage(ctx, events, "runtime.ready", 3*time.Minute)
	if err != nil {
		return result, err
	}
	var readyPayload struct {
		AgentSessionID       string `json:"agent_session_id"`
		AgentProfileID       string `json:"agent_profile_id"`
		AgentProfileRevision uint64 `json:"agent_profile_revision"`
		AgentProfileDigest   string `json:"agent_profile_digest"`
	}
	if json.Unmarshal(ready.Payload, &readyPayload) != nil || readyPayload.AgentSessionID == "" ||
		readyPayload.AgentProfileID != profile.ID || readyPayload.AgentProfileRevision != profile.Revision ||
		readyPayload.AgentProfileDigest != profile.Digest {
		return result, fmt.Errorf("runtime.ready profile or AgentSession is invalid")
	}
	result.AgentSessionID = readyPayload.AgentSessionID
	if expectInvalidCredential {
		turnID := "acceptance-kimi-invalid-turn-1"
		result.AgentTurnIDs = append(result.AgentTurnIDs, turnID)
		if err := sendTurn(ctx, hub, credential.Identity, runID, readyPayload.AgentSessionID, turnID,
			"Do not call any tool. Reply with exactly UNEXPECTED_AUTH_SUCCESS and nothing else."); err != nil {
			return result, err
		}
		_, err := waitForTurnTerminal(ctx, events, turnID)
		if !errors.Is(err, errAgentTurnFailed) {
			return result, fmt.Errorf("invalid credential did not produce an Agent error")
		}
		result.TerminalReasons = append(result.TerminalReasons, "error")
		result.ValidationMode = "expected_model_authentication_failure"
		result.ExecutionOutcome = "failed"
		result.AcceptanceOutcome = "pending"
		if err := assertTextOnlyWorkspace(storage.WorkingCopy.BackendPath); err != nil {
			return result, err
		}
		result.TraceSHA256, err = checksumFile(filepath.Join(storage.TraceSpool.BackendPath, "acp.raw.jsonl"))
		if err != nil {
			return result, err
		}
		return finalizeProfileInvariants(result, storage.WorkingCopy.BackendPath, 1, 1)
	}

	prompts := []string{
		fmt.Sprintf("Use the shell to run exactly: printf 'PROFILE=%s\\n' > /workspace/profile.txt . Then read /workspace/profile.txt and finish. Do not modify any other file.", profile.ID),
		fmt.Sprintf("Continue this same session. Use the shell to run exactly: printf 'VERIFIED=%s\\n' > /workspace/follow-up.txt . Then read both profile.txt and follow-up.txt and finish. Do not modify any other file.", profile.ID),
	}
	expectedResponses := []string{"", ""}
	result.ValidationMode = "workspace_tool_execution"
	result.ExecutionOutcome = "completed"
	result.AcceptanceOutcome = "passed"
	if profile.ID == config.DeepSeekCodexOpenAIV1 {
		prompts = []string{
			"Do not call any tool. Reply with exactly DEEPSEEK_TURN_1_OK and nothing else.",
			"Continue this same session. Do not call any tool. Reply with exactly DEEPSEEK_TURN_2_OK and nothing else.",
		}
		expectedResponses = []string{"DEEPSEEK_TURN_1_OK", "DEEPSEEK_TURN_2_OK"}
		result.ValidationMode = "same_session_text_only"
	}
	for turnIndex, prompt := range prompts {
		turnID := fmt.Sprintf("acceptance-%s-turn-%d", short, turnIndex+1)
		result.AgentTurnIDs = append(result.AgentTurnIDs, turnID)
		if err := sendTurn(ctx, hub, credential.Identity, runID, readyPayload.AgentSessionID, turnID, prompt); err != nil {
			return result, err
		}
		terminal, err := waitForTurnTerminal(ctx, events, turnID)
		if err != nil {
			return result, err
		}
		result.TerminalReasons = append(result.TerminalReasons, terminal.stopReason)
		if expected := expectedResponses[turnIndex]; expected != "" {
			if !strings.Contains(terminal.agentText, expected) {
				return result, fmt.Errorf("AgentTurn %s response assertion failed", turnID)
			}
			result.ResponseAssertions = append(result.ResponseAssertions, expected)
		}
		if opts.mode == "recovery" && turnIndex == 0 {
			result.RecoveryAttempted = true
			result.RecoveryScenario = "idle_between_turns_manual_replacement"
			productionRecoveryPath := false
			result.ProductionRecoveryPath = &productionRecoveryPath
			observation, cleanupErr := provisioner.Cleanup(
				ctx,
				application.RuntimeCleanupRequest{
					RuntimeInstanceID: credential.Identity.RuntimeInstanceID,
					Generation:        uint64(credential.Identity.RuntimeGeneration),
				},
			)
			if cleanupErr != nil || !observation.Confirmed {
				return result, errors.Join(
					cleanupErr,
					errors.New("failed generation cleanup was not proven before replacement"),
				)
			}
			hub.Revoke(credential.Identity)
			replacementRuntimeID := fmt.Sprintf("acceptance-%s-runtime-2", short)
			replacementIdentity := controlgrpc.RuntimeIdentity{
				WorkerSessionID: sessionID, RuntimeInstanceID: replacementRuntimeID,
				RuntimeGeneration: 2, ChannelEpoch: 3,
			}
			replacementCredential, err := controlgrpc.NewCredential(replacementIdentity)
			if err != nil {
				return result, err
			}
			if err := hub.RegisterCredential(replacementCredential); err != nil {
				return result, err
			}
			defer hub.Revoke(replacementIdentity)
			replacementEvents, err := hub.Events(ctx, replacementRuntimeID, 2)
			if err != nil {
				return result, err
			}
			replacementSecrets, err := (runtimesecret.Materializer{
				Ownership: runtimesecret.OSOwnership{}, OwnerUID: runtimesecret.OSOwnerUID,
			}).Materialize(runtimesecret.Request{
				WorkerSessionID: sessionID, WorkerRunID: runID,
				RuntimeInstanceID: replacementRuntimeID,
				Generation:        2, ChannelEpoch: 3,
				WorkerControlToken: replacementCredential.Token,
				AgentSessionID:     readyPayload.AgentSessionID, LoadExistingSession: true,
				SessionStorage: storage,
				AgentProfile: application.ProfileRef{
					ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
				},
				ProviderCredential: providerCredential,
			})
			if err != nil {
				return result, err
			}
			replacementBootstrapBytes, err := os.ReadFile(replacementSecrets.Bootstrap.BackendPath)
			if err != nil {
				return result, fmt.Errorf("read replacement Runtime bootstrap")
			}
			var replacementBootstrap runtimesecret.Bootstrap
			if err := json.Unmarshal(replacementBootstrapBytes, &replacementBootstrap); err != nil {
				return result, fmt.Errorf("decode replacement Runtime bootstrap")
			}
			*knownTokens = append(*knownTokens,
				secretcheck.KnownToken{
					Label: labelPrefix + "replacement-control",
					Value: replacementCredential.Token,
				},
				secretcheck.KnownToken{
					Label: labelPrefix + "replacement-proxy",
					Value: replacementBootstrap.ProxyToken,
				},
				secretcheck.KnownToken{
					Label: labelPrefix + "replacement-model-sink",
					Value: replacementBootstrap.ModelSinkToken,
				},
			)
			replacement, err := provisioner.Provision(ctx, application.RuntimeProvisionRequest{
				WorkerSessionID: sessionID, WorkerRunID: runID,
				RuntimeInstanceID: replacementRuntimeID, Generation: 2,
				ResourceProfile: application.ProfileRef{
					ID: resourceProfile.ID, Revision: resourceProfile.Revision,
					Digest: resourceProfile.Digest,
				},
				AgentProfile: application.ProfileRef{
					ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
				},
				NetworkProfile: application.ProfileRef{
					ID: networkProfile.ID, Revision: networkProfile.Revision,
					Digest: networkProfile.Digest,
				},
				NetworkEnforcementStatus: networkEvidence.EnforcementStatus,
				Storage:                  storage, Secrets: replacementSecrets,
			})
			if err != nil {
				return result, err
			}
			replacementReady, err := waitForMessage(
				ctx,
				replacementEvents,
				"runtime.ready",
				3*time.Minute,
			)
			if err != nil {
				return result, err
			}
			var replacementReadyPayload struct {
				AgentSessionID string `json:"agent_session_id"`
			}
			if json.Unmarshal(replacementReady.Payload, &replacementReadyPayload) != nil ||
				replacementReadyPayload.AgentSessionID != readyPayload.AgentSessionID {
				return result, fmt.Errorf("replacement Runtime did not preserve AgentSession")
			}
			credential = replacementCredential
			events = replacementEvents
			result.ReplacementRuntimeID = replacementRuntimeID
			result.ReplacementRuntimeGeneration = 2
			result.ReplacementContainerID = replacement.ContainerID
			result.AgentSessionPreserved = true
			result.runtimeIDs = append(result.runtimeIDs, replacementRuntimeID)
			runtimeGenerations[replacementRuntimeID] = 2
		}
	}
	if profile.ID == config.KimiCodexAnthropicV1 {
		if err := assertWorkspaceResult(storage.WorkingCopy.BackendPath, profile.ID); err != nil {
			return result, err
		}
	} else if err := assertTextOnlyWorkspace(storage.WorkingCopy.BackendPath); err != nil {
		return result, err
	}
	tracePath := filepath.Join(storage.TraceSpool.BackendPath, "acp.raw.jsonl")
	result.TraceSHA256, err = checksumFile(tracePath)
	if err != nil {
		return result, err
	}
	if opts.mode == "recovery" {
		rawTrace, readErr := os.ReadFile(tracePath)
		if readErr != nil {
			return result, fmt.Errorf("read recovery ACP Trace")
		}
		result.NoPromptReplay = bytes.Count(rawTrace, []byte("session/prompt")) == len(prompts)
		if !result.NoPromptReplay {
			return result, fmt.Errorf("recovery replayed or lost an AgentTurn prompt")
		}
	}
	expectedRuntimes := 1
	if opts.mode == "recovery" {
		expectedRuntimes = 2
	}
	return finalizeProfileInvariants(
		result,
		storage.WorkingCopy.BackendPath,
		len(prompts),
		expectedRuntimes,
	)
}

func sendTurn(ctx context.Context, hub *controlgrpc.Hub, identity controlgrpc.RuntimeIdentity, runID, agentSessionID, turnID, prompt string) error {
	payload, _ := json.Marshal(map[string]string{"prompt": prompt})
	messageID := "start-" + turnID
	return hub.Send(ctx, application.WorkerControlCommand{Envelope: contractsv1.RuntimeControlEnvelope{
		SchemaVersion: contractsv1.RuntimeControlSchemaVersion, MessageID: messageID, Kind: "command",
		WorkerSessionID: contractsv1.WorkerSessionID(identity.WorkerSessionID), WorkerRunID: contractsv1.WorkerRunID(runID),
		RuntimeInstanceID: contractsv1.RuntimeInstanceID(identity.RuntimeInstanceID), RuntimeGeneration: identity.RuntimeGeneration,
		AgentSessionID: contractsv1.AgentSessionID(agentSessionID), AgentTurnID: contractsv1.AgentTurnID(turnID),
		ChannelEpoch: identity.ChannelEpoch, IdempotencyKey: messageID, MessageType: "agent_turn.start", Payload: payload,
	}})
}

type turnTerminal struct {
	stopReason string
	agentText  string
}

func waitForTurnTerminal(ctx context.Context, events <-chan application.WorkerControlEvent, turnID string) (turnTerminal, error) {
	timer := time.NewTimer(turnTimeout)
	defer timer.Stop()
	var agentText strings.Builder
	for {
		select {
		case <-ctx.Done():
			return turnTerminal{}, ctx.Err()
		case <-timer.C:
			return turnTerminal{}, fmt.Errorf("AgentTurn %s timed out", turnID)
		case event := <-events:
			if event.Envelope.AgentTurnID != contractsv1.AgentTurnID(turnID) || event.Envelope.MessageType != "agent.update" {
				continue
			}
			var payload struct {
				UpdateKind string          `json:"update_kind"`
				Update     json.RawMessage `json:"update"`
			}
			if json.Unmarshal(event.Payload, &payload) != nil {
				return turnTerminal{}, fmt.Errorf("decode Agent update")
			}
			switch payload.UpdateKind {
			case "agent_message_chunk":
				var chunk struct {
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				if json.Unmarshal(payload.Update, &chunk) == nil {
					agentText.WriteString(chunk.Content.Text)
				}
			case "terminal":
				var terminal struct {
					StopReason string `json:"stop_reason"`
				}
				if json.Unmarshal(payload.Update, &terminal) != nil || terminal.StopReason == "" {
					return turnTerminal{}, fmt.Errorf("decode Agent terminal update")
				}
				return turnTerminal{stopReason: terminal.StopReason, agentText: agentText.String()}, nil
			case "error":
				return turnTerminal{}, errAgentTurnFailed
			}
		}
	}
}

func waitForMessage(ctx context.Context, events <-chan application.WorkerControlEvent, messageType string, timeout time.Duration) (application.WorkerControlEvent, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return application.WorkerControlEvent{}, ctx.Err()
		case <-timer.C:
			return application.WorkerControlEvent{}, fmt.Errorf("wait for %s timed out", messageType)
		case event := <-events:
			if event.Envelope.MessageType == messageType {
				return event, nil
			}
		}
	}
}

func writeFixture(root string) error {
	content, err := os.ReadFile(sourceFixturePath)
	if err != nil {
		return fmt.Errorf("read source acceptance fixture")
	}
	path := filepath.Join(root, "fixture.txt")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write acceptance fixture")
	}
	if err := os.Chown(path, runtimesecret.AgentUID, runtimesecret.AgentUID); err != nil {
		return fmt.Errorf("handoff acceptance fixture")
	}
	return nil
}

func assertWorkspaceResult(root, profileID string) error {
	want := map[string]string{
		"profile.txt":   "PROFILE=" + profileID + "\n",
		"follow-up.txt": "VERIFIED=" + profileID + "\n",
	}
	for name, expected := range want {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(content) != expected {
			return fmt.Errorf("Agent result %s is missing or invalid", name)
		}
	}
	return nil
}

func assertTextOnlyWorkspace(root string) error {
	fixture, err := os.ReadFile(filepath.Join(root, "fixture.txt"))
	source, sourceErr := os.ReadFile(sourceFixturePath)
	if err != nil || sourceErr != nil || !bytes.Equal(fixture, source) {
		return fmt.Errorf("text-only acceptance fixture changed")
	}
	for _, name := range []string{"profile.txt", "follow-up.txt"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("text-only AgentTurn unexpectedly changed Workspace")
		}
	}
	return nil
}

func finalizeProfileInvariants(
	result profileSummary,
	workspaceRoot string,
	expectedTurns int,
	expectedRuntimes int,
) (profileSummary, error) {
	var err error
	result.WorkspaceFixtureSHA256After, err = checksumFile(filepath.Join(workspaceRoot, "fixture.txt"))
	if err != nil {
		return result, err
	}
	result.FixtureImmutable = result.WorkspaceFixtureSHA256Before == result.WorkspaceFixtureSHA256After
	if !result.FixtureImmutable {
		return result, errors.New("Workspace acceptance fixture changed")
	}
	result.IdentityCardinality = identityCardinality{
		WorkerSessions: 1, WorkerRuns: 1, Runtimes: expectedRuntimes,
		AgentSessions: 1, AgentTurns: len(result.AgentTurnIDs),
	}
	runtimeIdentityVerified := result.RuntimeGeneration == 1 &&
		len(result.runtimeIDs) == expectedRuntimes &&
		uniqueStrings(result.runtimeIDs)
	if expectedRuntimes == 1 {
		runtimeIdentityVerified = runtimeIdentityVerified &&
			result.ReplacementRuntimeID == "" &&
			result.ReplacementRuntimeGeneration == 0 &&
			result.ReplacementContainerID == ""
	} else {
		runtimeIdentityVerified = runtimeIdentityVerified &&
			result.ReplacementRuntimeID != "" &&
			result.ReplacementRuntimeGeneration == uint64(expectedRuntimes) &&
			result.ReplacementContainerID != ""
	}
	result.IdentityCardinalityVerified = result.WorkerSessionID != "" && result.WorkerRunID != "" &&
		result.RuntimeID != "" && result.ContainerID != "" && result.AgentSessionID != "" &&
		runtimeIdentityVerified &&
		len(result.AgentTurnIDs) == expectedTurns &&
		len(result.TerminalReasons) == expectedTurns && uniqueStrings(result.AgentTurnIDs)
	if !result.IdentityCardinalityVerified {
		return result, errors.New("acceptance identity cardinality is invalid")
	}
	return result, nil
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func scanPersistedTree(ctx context.Context, scanner *secretcheck.Scanner, root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk persisted acceptance data")
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open persisted acceptance data")
		}
		defer file.Close()
		relative, _ := filepath.Rel(root, path)
		return scanner.ScanReader(ctx, "runtime-data:"+filepath.ToSlash(relative), file)
	})
}

func managedResidue(
	ctx context.Context,
	engine *client.Client,
	runtimeIDs []string,
) (int, int, error) {
	containers, err := engine.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return 0, 0, fmt.Errorf("list managed Runtime containers")
	}
	volumes, err := engine.VolumeList(ctx, client.VolumeListOptions{})
	if err != nil {
		return 0, 0, fmt.Errorf("list managed Runtime volumes")
	}
	containerCount := 0
	managed := make(map[string]struct{}, len(runtimeIDs))
	for _, runtimeID := range runtimeIDs {
		managed[runtimeID] = struct{}{}
	}
	for _, container := range containers.Items {
		if _, ok := managed[container.Labels["com.switchback.runtime-instance-id"]]; ok {
			containerCount++
		}
	}
	volumeCount := 0
	for _, volume := range volumes.Items {
		if _, ok := managed[volume.Labels["com.switchback.runtime-instance-id"]]; ok {
			volumeCount++
		}
	}
	return containerCount, volumeCount, nil
}

func readCredential(path string) (runtimesecret.ProviderCredential, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > 4<<10 {
		return nil, fmt.Errorf("provider credential file is invalid")
	}
	content, err := os.ReadFile(path)
	content = bytes.TrimSuffix(content, []byte{'\n'})
	if err != nil || len(content) == 0 || strings.ContainsAny(string(content), "\r\n\x00") {
		return nil, fmt.Errorf("provider credential file is invalid")
	}
	return runtimesecret.ProviderCredential(content), nil
}

func checksumFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil || len(content) == 0 {
		return "", fmt.Errorf("read authoritative ACP Trace")
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), nil
}

func writeSummary(path string, summary acceptanceSummary) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("prepare acceptance summary directory")
	}
	encoded, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode acceptance summary")
	}
	encoded = append(encoded, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write acceptance summary")
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("commit acceptance summary")
	}
	return nil
}

func loadOptions(lookupEnv func(string) (string, bool)) (options, error) {
	required := func(name string) (string, error) {
		value, ok := lookupEnv(name)
		if !ok || value == "" || strings.TrimSpace(value) != value {
			return "", fmt.Errorf("%s is required", name)
		}
		return value, nil
	}
	result := options{mode: "success"}
	if mode, ok := lookupEnv("SWITCHBACK_ACCEPTANCE_MODE"); ok {
		result.mode = mode
	}
	if result.mode != "success" && result.mode != "invalid-credential" && result.mode != "recovery" {
		return options{}, errors.New("SWITCHBACK_ACCEPTANCE_MODE is unsupported")
	}
	var err error
	for name, target := range map[string]*string{
		"SWITCHBACK_ACCEPTANCE_DATA_ROOT":             &result.dataRoot,
		"SWITCHBACK_ACCEPTANCE_ENGINE_DATA_ROOT":      &result.engineDataRoot,
		"SWITCHBACK_ACCEPTANCE_WORKER_IMAGE":          &result.workerImage,
		"SWITCHBACK_ACCEPTANCE_NETWORK":               &result.network,
		"SWITCHBACK_ACCEPTANCE_BACKEND_GRPC_TARGET":   &result.grpcTarget,
		"SWITCHBACK_ACCEPTANCE_BACKEND_HTTP_BASE_URL": &result.httpBaseURL,
		"SWITCHBACK_ACCEPTANCE_SUMMARY":               &result.summaryPath,
	} {
		*target, err = required(name)
		if err != nil {
			return options{}, err
		}
	}
	if result.mode == "success" {
		result.kimiKeyPath, err = required("SWITCHBACK_ACCEPTANCE_KIMI_KEY_FILE")
		if err != nil {
			return options{}, err
		}
	}
	if result.mode == "success" || result.mode == "recovery" {
		result.deepSeekKeyPath, err = required("SWITCHBACK_ACCEPTANCE_DEEPSEEK_KEY_FILE")
		if err != nil {
			return options{}, err
		}
	}
	for _, path := range []string{result.dataRoot, result.engineDataRoot, result.kimiKeyPath, result.deepSeekKeyPath, result.summaryPath} {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return options{}, fmt.Errorf("acceptance paths must be clean and absolute")
		}
	}
	if generation := strconv.FormatUint(1, 10); generation != "1" {
		return options{}, fmt.Errorf("invalid acceptance generation")
	}
	return result, nil
}

func zero(content []byte) {
	for index := range content {
		content[index] = 0
	}
}
