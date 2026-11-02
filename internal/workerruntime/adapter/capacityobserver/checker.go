// Package capacityobserver checks authenticated finite created-layer observations.
// It cannot grant capacity or execution authority.
package capacityobserver

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/abietic/switchback/internal/platform/capacityprobe"
	"github.com/abietic/switchback/internal/platform/createdlayeripc"
	"github.com/abietic/switchback/internal/workerruntime/adapter/capacitysnapshot"
	workerapp "github.com/abietic/switchback/internal/workerruntime/application"
)

// Match the fixed IPC wire grammar before touching its key or transport.
var ipcOwnerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

type observeFunc func(context.Context, createdlayeripc.Binding) (capacityprobe.CreatedLayerCollection, error)

// Checker owns immutable policy and a lazy fixed IPC client. initialize is an
// instance-local package test seam; production always installs the fixed client.
type Checker struct {
	verifier            *capacitysnapshot.CreatedLayerVerifier
	manifestDigest      string
	coreCovered         bool
	once                sync.Once
	initialize          func() (observeFunc, error)
	observe             observeFunc
	initializationError error
}

var _ workerapp.InitialCreatedLayerChecker = (*Checker)(nil)

// New performs no IO. Key/client initialization happens only on the first legal
// request with exact aggregate core coverage, and its result stays pinned.
func New(trust capacitysnapshot.Trust) (*Checker, error) {
	verifier, err := capacitysnapshot.NewCreatedLayerVerifier(trust)
	if err != nil {
		return nil, err
	}
	digest := trust.ManifestDigest
	c := &Checker{verifier: verifier, manifestDigest: digest}
	for _, core := range trust.Core {
		if core.CgroupPath == createdlayeripc.CoreCgroupPath {
			c.coreCovered = true
		}
	}
	c.initialize = func() (observeFunc, error) {
		key, err := createdlayeripc.ReadClientKey()
		if err != nil {
			return nil, err
		}
		defer clear(key)
		client, err := createdlayeripc.NewClient(key, digest)
		if err != nil {
			return nil, err
		}
		return client.Observe, nil
	}
	return c, nil
}

func (c *Checker) CheckCreatedLayer(ctx context.Context, request workerapp.RuntimeProvisionRequest, identity workerapp.CreatedRuntimeIdentity) error {
	if ctx == nil {
		return fmt.Errorf("created-layer context unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("created-layer checker unconfigured")
	}
	if err := c.verifier.ValidateRequest(ctx, request, identity); err != nil {
		return err
	}
	if !ipcOwnerID.MatchString(request.RuntimeInstanceID) || !ipcOwnerID.MatchString(request.WorkerSessionID) || !ipcOwnerID.MatchString(request.WorkerRunID) {
		return fmt.Errorf("created-layer IPC request identity invalid")
	}
	if !c.coreCovered {
		return fmt.Errorf("created-layer observer aggregate core coverage unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only initialization is cached. No request context enters sync.Once: a first
	// request canceled during initialization must not poison the next request.
	c.once.Do(func() {
		if c.initialize == nil {
			c.initializationError = fmt.Errorf("created-layer client unconfigured")
			return
		}
		c.observe, c.initializationError = c.initialize()
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.initializationError != nil || c.observe == nil {
		return fmt.Errorf("created-layer observer initialization unavailable")
	}
	binding := createdlayeripc.Binding{ManifestDigest: c.manifestDigest, ContainerID: identity.ContainerID, RuntimeID: request.RuntimeInstanceID, SessionID: request.WorkerSessionID, RunID: request.WorkerRunID, Generation: request.Generation, ProfileID: request.ResourceProfile.ID, ProfileRevision: request.ResourceProfile.Revision, ProfileDigest: request.ResourceProfile.Digest}
	collection, err := c.observe(ctx, binding)
	if cancel := ctx.Err(); cancel != nil {
		return cancel
	}
	if err != nil {
		return fmt.Errorf("created-layer authenticated observation unavailable")
	}
	facts, err := observationFacts(binding, collection)
	if cancel := ctx.Err(); cancel != nil {
		return cancel
	}
	if err != nil {
		return err
	}
	return c.verifier.VerifyCreatedLayerFacts(ctx, facts, request, identity)
}
