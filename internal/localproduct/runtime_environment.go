package localproduct

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abietic/switchback/internal/platform/config"
	"github.com/abietic/switchback/internal/workerruntime/adapter/controlgrpc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/localstorage"
	"github.com/abietic/switchback/internal/workerruntime/adapter/runtimesecret"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

type preparedRuntime struct {
	Storage      workerapp.SessionStorage
	Credential   controlgrpc.Credential
	Secrets      workerapp.RuntimeSecretResources
	SourceDigest string
}

// Only database availability/transaction conflicts justify retrying the same
// preparation. Authentication, identity, policy and schema failures do not.
func retryableReplacementBind(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "40001", "40P01", "53300", "57P01", "57P02", "57P03", "08000", "08001", "08003", "08006", "08007":
			return true
		}
		return false
	}
	var networkError *net.OpError
	return pgconn.SafeToRetry(err) || errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

type liveRuntimeRestoreRequest struct {
	WorkerSessionID             string
	WorkerRunID                 string
	BootstrapWorkerRunID        string
	RuntimeInstanceID           string
	RuntimeGeneration           uint64
	ControlChannelEpoch         uint64
	AgentSessionID              string
	AgentProfileID              string
	AgentProfileRevision        uint64
	AgentProfileDigest          string
	WorkingCopyCheckpointDigest string
}

type runtimeEnvironment struct {
	configuration   Config
	hub             *controlgrpc.Hub
	profiles        config.ProfileRegistry
	networkProfiles config.RuntimeNetworkProfileRegistry
	storage         localstorage.Materializer
	secrets         runtimesecret.Materializer

	mu       sync.Mutex
	prepared map[string]preparedRuntime
}

func newRuntimeEnvironment(configuration Config, hub *controlgrpc.Hub) *runtimeEnvironment {
	return &runtimeEnvironment{
		configuration: configuration, hub: hub, profiles: config.ProductionProfileRegistry(),
		networkProfiles: config.ProductionRuntimeNetworkProfiles(),
		storage: localstorage.Materializer{
			BackendDataRoot: configuration.DataRoot, EngineDataRoot: configuration.EngineDataRoot,
			Ownership: localstorage.OSOwnership{},
		},
		secrets:  runtimesecret.Materializer{Ownership: runtimesecret.OSOwnership{}, OwnerUID: runtimesecret.OSOwnerUID},
		prepared: make(map[string]preparedRuntime),
	}
}

// PrepareRuntime is a private convenience for existing callers. Production
// coordination must pin the readonly plan through PlanRuntime before Prepare.
func (environment *runtimeEnvironment) PrepareRuntime(
	ctx context.Context,
	request workerapp.RuntimePreparationRequest,
) (result workerapp.RuntimeProvisionRequest, resultErr error) {
	plan, err := environment.PlanRuntime(ctx, request)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, plan.Close()) }()
	return plan.Prepare(ctx)
}

func (environment *runtimeEnvironment) preparePlannedRuntime(
	ctx context.Context,
	request workerapp.RuntimePreparationRequest,
	sourcePlan *workspaceSourcePlan,
	binding workerapp.RuntimeSourcePlanBinding,
	profiles runtimePreparationProfiles,
) (_ workerapp.RuntimeProvisionRequest, resultErr error) {
	ownership := request.Ownership
	profile, resource, network := profiles.agent, profiles.resource, profiles.network
	if err := ctx.Err(); err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	var sourceDigest string
	storage, err := environment.storage.MaterializeWithBootstrap(ownership.WorkerSessionID, func(workingCopy string) error {
		var copyErr error
		sourceDigest, copyErr = sourcePlan.Copy(ctx, workingCopy, (*os.File).Chown)
		return copyErr
	})
	if err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	cleanupStorage := true
	defer func() {
		if resultErr != nil && cleanupStorage {
			if cleanupErr := os.RemoveAll(storage.BackendRoot); cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove partial WorkerSession storage: %w", cleanupErr))
			}
		}
	}()
	identity := controlgrpc.RuntimeIdentity{
		WorkerSessionID: ownership.WorkerSessionID, RuntimeInstanceID: ownership.RuntimeInstanceID,
		RuntimeGeneration: uint32(ownership.RuntimeGeneration), ChannelEpoch: ownership.ControlChannelEpoch,
	}
	credential, err := controlgrpc.NewCredential(identity)
	if err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	if err := environment.hub.RegisterCredential(credential); err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	credentialRegistered := true
	registryMaterialized := false
	defer func() {
		if resultErr != nil && credentialRegistered {
			if registryMaterialized {
				// Accepted original scope survives a failed fence for retry.
				cleanupStorage = false
				resultErr = errors.Join(resultErr, environment.fenceObservationRegistry(context.Background(), identity))
			} else {
				environment.hub.Revoke(identity)
			}
		}
	}()
	providerCredential, err := readProviderCredential(environment.credentialFile(profile.ID))
	if err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	defer zero(providerCredential)
	secretResources, err := environment.secrets.Materialize(runtimesecret.Request{
		WorkerSessionID: ownership.WorkerSessionID, WorkerRunID: ownership.WorkerRunID,
		RuntimeInstanceID: ownership.RuntimeInstanceID, Generation: ownership.RuntimeGeneration,
		ChannelEpoch: ownership.ControlChannelEpoch, WorkerControlToken: credential.Token,
		SessionStorage:     storage,
		AgentProfile:       workerapp.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest},
		ProviderCredential: providerCredential,
	})
	if err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	registryMaterialized = true
	prepared := preparedRuntime{
		Storage: storage, Credential: credential, Secrets: secretResources,
		SourceDigest: sourceDigest,
	}
	environment.mu.Lock()
	if _, exists := environment.prepared[ownership.RuntimeInstanceID]; exists {
		environment.mu.Unlock()
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("Runtime environment identity already exists")
	}
	environment.prepared[ownership.RuntimeInstanceID] = prepared
	environment.mu.Unlock()
	cleanupStorage = false
	credentialRegistered = false
	return workerapp.RuntimeProvisionRequest{
		WorkerSessionID: ownership.WorkerSessionID, WorkerRunID: ownership.WorkerRunID,
		RuntimeInstanceID: ownership.RuntimeInstanceID, Generation: ownership.RuntimeGeneration,
		ResourceProfile: workerapp.ProfileRef{ID: resource.ID, Revision: resource.Revision, Digest: resource.Digest},
		AgentProfile:    workerapp.ProfileRef{ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest},
		NetworkProfile: workerapp.ProfileRef{
			ID: network.ID, Revision: network.Revision, Digest: network.Digest,
		},
		SourcePlanDigest:         binding.Digest,
		NetworkEnforcementStatus: network.EnforcementStatus,
		Storage:                  storage, Secrets: secretResources,
	}, nil
}

func (environment *runtimeEnvironment) PrepareReplacementRuntime(
	ctx context.Context,
	state workerapp.RuntimeRecoveryState,
) (_ workerapp.RuntimeProvisionRequest, resultErr error) {
	if environment == nil || environment.hub == nil {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("Runtime environment is unavailable")
	}
	if state.Phase != "provisioning" || state.ReplacementRuntimeInstanceID == "" ||
		state.ReplacementGeneration != state.FailedGeneration+1 ||
		state.ReplacementChannelEpoch <= state.FailedChannelEpoch ||
		state.RuntimeWorkerRunID == "" || state.AgentSessionID == "" {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("replacement Runtime ownership is incomplete")
	}
	profileRevision, err := strconv.ParseUint(state.AgentProfileRevision, 10, 64)
	if err != nil || profileRevision == 0 {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("replacement AgentProfile revision is invalid")
	}
	profile, err := environment.profiles.ResolveAgent(config.AgentProfileRef{
		ID: state.AgentProfileID, Revision: profileRevision, Digest: state.AgentProfileDigest,
	})
	if err != nil {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("replacement AgentProfile is unavailable or stale")
	}
	resource, ok := environment.profiles.RuntimeResource(state.ResourceProfileID)
	if !ok {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("replacement Runtime resource profile is unavailable")
	}
	network, err := environment.networkProfiles.ResolveStored(config.RuntimeNetworkEvidence{
		ID: state.NetworkPolicyID, Revision: state.NetworkPolicyRevision,
		Digest: state.NetworkPolicyDigest, EnforcementStatus: state.NetworkEnforcementStatus,
	})
	if err != nil {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("replacement Runtime network profile is unavailable or stale")
	}
	storage, err := environment.storage.OpenExisting(state.WorkerSessionID)
	if err != nil {
		return workerapp.RuntimeProvisionRequest{}, err
	}
	sourceDigest := strings.TrimPrefix(state.WorkingCopyCheckpointDigest, "sha256:")
	if len(sourceDigest) != 64 || "sha256:"+sourceDigest != state.WorkingCopyCheckpointDigest {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("replacement WorkingCopy checkpoint is invalid")
	}
	environment.mu.Lock()
	existing, found := environment.prepared[state.ReplacementRuntimeInstanceID]
	environment.mu.Unlock()
	if found {
		return replacementProvisionRequest(state, resource, profile, network, existing), nil
	}
	identity := controlgrpc.RuntimeIdentity{
		WorkerSessionID:   state.WorkerSessionID,
		RuntimeInstanceID: state.ReplacementRuntimeInstanceID,
		RuntimeGeneration: uint32(state.ReplacementGeneration),
		ChannelEpoch:      state.ReplacementChannelEpoch,
	}
	profileRef := workerapp.ProfileRef{
		ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
	}
	var credential controlgrpc.Credential
	var secretResources workerapp.RuntimeSecretResources
	bootstrapPath := filepath.Join(
		storage.BackendRoot, "runtimes",
		state.ReplacementRuntimeInstanceID+"-g"+strconv.FormatUint(state.ReplacementGeneration, 10),
		"secret-input", "bootstrap.json",
	)
	if _, statErr := os.Lstat(bootstrapPath); statErr == nil {
		var bootstrap runtimesecret.Bootstrap
		secretResources, bootstrap, err = environment.secrets.OpenExisting(runtimesecret.ExistingRequest{
			WorkerSessionID: state.WorkerSessionID, WorkerRunID: state.RuntimeWorkerRunID,
			RuntimeInstanceID: state.ReplacementRuntimeInstanceID,
			Generation:        state.ReplacementGeneration, ChannelEpoch: state.ReplacementChannelEpoch,
			AgentSessionID: state.AgentSessionID, SessionStorage: storage, AgentProfile: profileRef,
		})
		if err != nil {
			return workerapp.RuntimeProvisionRequest{}, err
		}
		credential = controlgrpc.Credential{Identity: identity, Token: bootstrap.WorkerControlToken}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("inspect replacement Runtime bootstrap: %w", statErr)
	} else {
		credential, err = controlgrpc.NewCredential(identity)
		if err != nil {
			return workerapp.RuntimeProvisionRequest{}, err
		}
		providerCredential, err := readProviderCredential(environment.credentialFile(profile.ID))
		if err != nil {
			return workerapp.RuntimeProvisionRequest{}, err
		}
		defer zero(providerCredential)
		secretResources, err = environment.secrets.Materialize(runtimesecret.Request{
			WorkerSessionID: state.WorkerSessionID, WorkerRunID: state.RuntimeWorkerRunID,
			RuntimeInstanceID: state.ReplacementRuntimeInstanceID,
			Generation:        state.ReplacementGeneration, ChannelEpoch: state.ReplacementChannelEpoch,
			WorkerControlToken: credential.Token, AgentSessionID: state.AgentSessionID,
			LoadExistingSession: true, SessionStorage: storage, AgentProfile: profileRef,
			ProviderCredential: providerCredential,
		})
		if err != nil {
			return workerapp.RuntimeProvisionRequest{}, err
		}
	}
	credentialRegistered := true
	defer func() {
		if resultErr != nil && credentialRegistered && !errors.Is(resultErr, workerapp.ErrRuntimePreparationRetryable) {
			resultErr = errors.Join(resultErr, environment.fenceObservationRegistry(context.Background(), identity))
		}
	}()
	if err := environment.hub.RegisterCredential(credential); err != nil {
		if retryableReplacementBind(err) {
			return workerapp.RuntimeProvisionRequest{}, workerapp.ErrRuntimePreparationRetryable
		}
		return workerapp.RuntimeProvisionRequest{}, err
	}
	prepared := preparedRuntime{
		Storage: storage, Credential: credential, Secrets: secretResources,
		SourceDigest: sourceDigest,
	}
	environment.mu.Lock()
	if _, exists := environment.prepared[state.ReplacementRuntimeInstanceID]; exists {
		environment.mu.Unlock()
		return workerapp.RuntimeProvisionRequest{}, fmt.Errorf("replacement Runtime environment identity already exists")
	}
	environment.prepared[state.ReplacementRuntimeInstanceID] = prepared
	environment.mu.Unlock()
	credentialRegistered = false
	return replacementProvisionRequest(state, resource, profile, network, prepared), nil
}

// RestoreLiveRuntime reconstructs only Backend-owned ephemeral state for a
// container that survived a Backend restart. It opens existing storage and
// bootstrap bytes, then rebinds the exact persisted credential; it never
// creates a token, rewrites a secret, or mutates the WorkingCopy.
func (environment *runtimeEnvironment) RestoreLiveRuntime(
	_ context.Context,
	request liveRuntimeRestoreRequest,
) (_ preparedRuntime, resultErr error) {
	if environment == nil || environment.hub == nil {
		return preparedRuntime{}, fmt.Errorf("Runtime environment is unavailable")
	}
	if request.WorkerSessionID == "" || request.WorkerRunID == "" ||
		request.RuntimeInstanceID == "" || request.RuntimeGeneration == 0 ||
		request.RuntimeGeneration > uint64(^uint32(0)) ||
		request.ControlChannelEpoch == 0 || request.AgentSessionID == "" ||
		request.AgentProfileID == "" || request.AgentProfileRevision == 0 {
		return preparedRuntime{}, fmt.Errorf("live Runtime restore identity is incomplete")
	}
	profile, err := environment.profiles.ResolveAgent(config.AgentProfileRef{
		ID:       request.AgentProfileID,
		Revision: request.AgentProfileRevision,
		Digest:   request.AgentProfileDigest,
	})
	if err != nil {
		return preparedRuntime{}, fmt.Errorf("live Runtime AgentProfile is unavailable or stale")
	}
	sourceDigest := strings.TrimPrefix(request.WorkingCopyCheckpointDigest, "sha256:")
	if len(sourceDigest) != 64 ||
		request.WorkingCopyCheckpointDigest != "sha256:"+sourceDigest {
		return preparedRuntime{}, fmt.Errorf("live Runtime WorkingCopy checkpoint is invalid")
	}
	storage, err := environment.storage.OpenExisting(request.WorkerSessionID)
	if err != nil {
		return preparedRuntime{}, err
	}
	profileRef := workerapp.ProfileRef{
		ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
	}
	bootstrapWorkerRunID := request.BootstrapWorkerRunID
	if bootstrapWorkerRunID == "" {
		bootstrapWorkerRunID = request.WorkerRunID
	}
	secretResources, bootstrap, err := environment.secrets.OpenLiveExisting(
		runtimesecret.LiveExistingRequest{
			WorkerSessionID: request.WorkerSessionID, WorkerRunID: bootstrapWorkerRunID,
			RuntimeInstanceID: request.RuntimeInstanceID, Generation: request.RuntimeGeneration,
			ChannelEpoch: request.ControlChannelEpoch, AgentSessionID: request.AgentSessionID,
			SessionStorage: storage, AgentProfile: profileRef,
		},
	)
	if err != nil {
		return preparedRuntime{}, err
	}
	credential := controlgrpc.Credential{
		Identity: controlgrpc.RuntimeIdentity{
			WorkerSessionID: request.WorkerSessionID, RuntimeInstanceID: request.RuntimeInstanceID,
			RuntimeGeneration: uint32(request.RuntimeGeneration),
			ChannelEpoch:      request.ControlChannelEpoch,
		},
		Token: bootstrap.WorkerControlToken,
	}
	prepared := preparedRuntime{
		Storage: storage, Credential: credential, Secrets: secretResources,
		SourceDigest: sourceDigest,
	}
	environment.mu.Lock()
	existing, found := environment.prepared[request.RuntimeInstanceID]
	environment.mu.Unlock()
	if found {
		if existing != prepared {
			return preparedRuntime{}, fmt.Errorf("live Runtime environment identity conflicts")
		}
		return existing, nil
	}
	if err := environment.hub.RegisterCredential(credential); err != nil {
		return preparedRuntime{}, err
	}
	registered := true
	defer func() {
		if resultErr != nil && registered {
			environment.hub.Revoke(credential.Identity)
		}
	}()
	environment.mu.Lock()
	if existing, found = environment.prepared[request.RuntimeInstanceID]; found {
		environment.mu.Unlock()
		// Another restore owns the same idempotent credential binding. Never
		// fence it while resolving this local insertion race.
		registered = false
		if existing != prepared {
			return preparedRuntime{}, fmt.Errorf("live Runtime environment identity conflicts")
		}
		return existing, nil
	}
	environment.prepared[request.RuntimeInstanceID] = prepared
	environment.mu.Unlock()
	registered = false
	return prepared, nil
}

func replacementProvisionRequest(
	state workerapp.RuntimeRecoveryState,
	resource config.RuntimeResourceProfile,
	profile config.AgentProfile,
	network config.RuntimeNetworkEvidence,
	prepared preparedRuntime,
) workerapp.RuntimeProvisionRequest {
	return workerapp.RuntimeProvisionRequest{
		WorkerSessionID: state.WorkerSessionID, WorkerRunID: state.RuntimeWorkerRunID,
		RuntimeInstanceID: state.ReplacementRuntimeInstanceID,
		Generation:        state.ReplacementGeneration,
		ResourceProfile: workerapp.ProfileRef{
			ID: resource.ID, Revision: resource.Revision, Digest: resource.Digest,
		},
		AgentProfile: workerapp.ProfileRef{
			ID: profile.ID, Revision: profile.Revision, Digest: profile.Digest,
		},
		NetworkProfile: workerapp.ProfileRef{
			ID: network.ID, Revision: network.Revision, Digest: network.Digest,
		},
		NetworkEnforcementStatus: network.EnforcementStatus,
		Storage:                  prepared.Storage, Secrets: prepared.Secrets,
	}
}

func (environment *runtimeEnvironment) fenceFailedRuntime(
	state workerapp.RuntimeRecoveryState,
) (workerapp.SessionStorage, error) {
	if state.FailedGeneration == 0 || state.FailedGeneration > uint64(^uint32(0)) {
		return workerapp.SessionStorage{}, errObservationCleanupFenceUnproven
	}
	identity := controlgrpc.RuntimeIdentity{
		WorkerSessionID:   state.WorkerSessionID,
		RuntimeInstanceID: state.FailedRuntimeInstanceID,
		RuntimeGeneration: uint32(state.FailedGeneration),
		ChannelEpoch:      state.FailedChannelEpoch,
	}
	if err := environment.fenceObservationRegistry(context.Background(), identity); err != nil {
		return workerapp.SessionStorage{}, err
	}
	return environment.storage.OpenExisting(state.WorkerSessionID)
}

// cleanupDurablyFencedRuntime removes only ephemeral/local authority after the
// exact Runtime-control fence has already committed in PostgreSQL. It must not
// call Hub.Revoke, because that would attempt a second durable fence and make
// local cleanup depend on an in-memory credential surviving Backend restart.
func (environment *runtimeEnvironment) cleanupDurablyFencedRuntime(
	authority workerapp.BackendDeadlineSourceAuthority,
	state workerapp.RuntimeControlChannelState,
) (workerapp.SessionStorage, error) {
	if environment == nil || environment.hub == nil ||
		authority.RuntimeGeneration == 0 ||
		authority.RuntimeGeneration > uint64(^uint32(0)) {
		return workerapp.SessionStorage{},
			fmt.Errorf("Backend deadline Runtime environment is unavailable")
	}
	identity := controlgrpc.RuntimeIdentity{
		WorkerSessionID:   authority.WorkerSessionID,
		RuntimeInstanceID: authority.RuntimeInstanceID,
		RuntimeGeneration: uint32(authority.RuntimeGeneration),
		ChannelEpoch:      authority.ChannelEpoch,
	}
	if err := environment.cleanupFencedObservationRegistry(context.Background(), identity, state); err != nil {
		return workerapp.SessionStorage{}, err
	}
	environment.hub.RevokeDurablyFenced(identity)
	return environment.storage.OpenExisting(authority.WorkerSessionID)
}

func (environment *runtimeEnvironment) get(runtimeID string) (preparedRuntime, bool) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	prepared, ok := environment.prepared[runtimeID]
	return prepared, ok
}

func (environment *runtimeEnvironment) discard(identity controlgrpc.RuntimeIdentity) error {
	environment.mu.Lock()
	prepared, ok := environment.prepared[identity.RuntimeInstanceID]
	environment.mu.Unlock()
	if ok && prepared.Credential.Identity != identity {
		return errObservationCleanupFenceUnproven
	}
	if !ok {
		// Failure before materialization has no registry to remove. Validate the
		// server-derived path even for this absence check; never infer a fence.
		if !observationCleanupIdentityValid(identity) {
			return errObservationCleanupFenceUnproven
		}
		sessionRoot := filepath.Join(environment.storage.BackendDataRoot, "sessions", identity.WorkerSessionID)
		if _, err := os.Lstat(sessionRoot); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		storage, err := environment.storage.OpenExisting(identity.WorkerSessionID)
		if err != nil {
			return err
		}
		runtimeRoot := filepath.Join(storage.BackendRoot, "runtimes", identity.RuntimeInstanceID+"-g"+strconv.FormatUint(uint64(identity.RuntimeGeneration), 10))
		if _, err := os.Lstat(runtimeRoot); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	// Durable Session data is retained; only this exact Runtime is discarded.
	return environment.fenceObservationRegistry(context.Background(), identity)
}

func (environment *runtimeEnvironment) credentialFile(profileID string) string {
	if profileID == config.KimiCodexAnthropicV1 {
		return environment.configuration.KimiCredentialFile
	}
	return environment.configuration.DeepSeekCredentialFile
}

func readProviderCredential(path string) (runtimesecret.ProviderCredential, error) {
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

func zero(content []byte) {
	for index := range content {
		content[index] = 0
	}
}

var _ workerapp.RuntimeEnvironmentPreparer = (*runtimeEnvironment)(nil)
