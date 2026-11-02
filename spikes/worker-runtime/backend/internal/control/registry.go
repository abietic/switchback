package control

import (
	"context"
	"errors"
	"sync"

	"google.golang.org/protobuf/proto"
	workercontrolv1 "switchback.local/spikes/worker-runtime/gen/workercontrol/v1"
)

const commandQueueCapacity = 32

var (
	ErrAlreadyConnected = errors.New("runtime already has an active control stream")
	ErrNotConnected     = errors.New("runtime control stream is not connected")
	ErrBackpressure     = errors.New("runtime command queue is full")
)

// Connection is the complete live state for one runtime control stream.
type Connection struct {
	Identity     Identity
	SendSequence uint64
	RecvSequence uint64
	PeerAck      uint64
	Commands     chan *workercontrolv1.BackendEnvelope
	Ready        chan struct{}

	mu sync.Mutex

	lifecycleMu   sync.Mutex
	lifecycleCond *sync.Cond
	inFlight      uint64
	fenced        bool
	closeErr      error
	done          chan struct{}
	released      chan struct{}
	readyOnce     sync.Once
	finalizeOnce  sync.Once
}

func newConnection(identity Identity) *Connection {
	connection := &Connection{
		Identity: identity,
		Commands: make(chan *workercontrolv1.BackendEnvelope, commandQueueCapacity),
		Ready:    make(chan struct{}),
		done:     make(chan struct{}),
		released: make(chan struct{}),
	}
	connection.lifecycleCond = sync.NewCond(&connection.lifecycleMu)
	return connection
}

func (c *Connection) beginFence(err error) {
	c.lifecycleMu.Lock()
	if !c.fenced {
		c.fenced = true
		c.closeErr = err
		close(c.done)
	}
	c.lifecycleMu.Unlock()
}

func (c *Connection) waitForDrain() {
	c.lifecycleMu.Lock()
	for c.inFlight != 0 {
		c.lifecycleCond.Wait()
	}
	c.lifecycleMu.Unlock()
}

func (c *Connection) drained() bool {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.inFlight == 0
}

func (c *Connection) admit() (*connectionOperation, error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.fenced {
		if c.closeErr != nil {
			return nil, c.closeErr
		}
		return nil, ErrNotConnected
	}
	c.inFlight++
	return &connectionOperation{connection: c}, nil
}

type connectionOperation struct {
	connection *Connection
	once       sync.Once
}

func (o *connectionOperation) release() {
	if o == nil || o.connection == nil {
		return
	}
	o.once.Do(func() {
		connection := o.connection
		connection.lifecycleMu.Lock()
		connection.inFlight--
		if connection.inFlight == 0 {
			connection.lifecycleCond.Broadcast()
		}
		connection.lifecycleMu.Unlock()
	})
}

func (c *Connection) terminalError() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.closeErr
}

func (c *Connection) terminalStatus() (error, bool) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.closeErr, c.fenced
}

func (c *Connection) markReady() {
	c.readyOnce.Do(func() { close(c.Ready) })
}

func (c *Connection) enqueue(command *workercontrolv1.BackendEnvelope) (error, bool) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.fenced {
		if c.closeErr != nil {
			return c.closeErr, false
		}
		return ErrNotConnected, false
	}
	select {
	case c.Commands <- command:
		return nil, false
	default:
		return ErrBackpressure, true
	}
}

// Registry owns credentials and at most one live stream per runtime.
type Registry struct {
	auth *Authenticator

	mu          sync.RWMutex
	connections map[Identity]*Connection
	changed     chan struct{}
}

func NewRegistry() *Registry {
	return &Registry{
		auth:        NewAuthenticator(),
		connections: make(map[Identity]*Connection),
		changed:     make(chan struct{}),
	}
}

func (r *Registry) RegisterCredential(credential Credential) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auth.Register(credential)
}

func (r *Registry) Authenticate(ctx context.Context) (Identity, error) {
	return r.auth.Authenticate(ctx)
}

func (r *Registry) Revoke(identity Identity) {
	r.mu.Lock()
	r.auth.Revoke(identity)
	connection := r.connections[identity]
	if connection != nil {
		connection.beginFence(ErrUnauthenticated)
	}
	r.notifyChangedLocked()
	r.mu.Unlock()
	if connection != nil {
		r.startFinalizer(identity, connection)
		<-connection.released
	}
}

func (r *Registry) Send(ctx context.Context, identity Identity, command *workercontrolv1.BackendEnvelope) error {
	if command == nil || command.GetPayload() == nil {
		return errors.New("backend command payload is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	cloned := proto.Clone(command).(*workercontrolv1.BackendEnvelope)
	r.mu.RLock()
	connection := r.connections[identity]
	authorized := r.auth.Contains(identity)
	if !authorized {
		r.mu.RUnlock()
		return ErrUnauthenticated
	}
	if connection == nil {
		r.mu.RUnlock()
		return ErrNotConnected
	}
	err, publishBackpressure := connection.enqueue(cloned)
	r.mu.RUnlock()
	if publishBackpressure {
		r.fenceAndWait(identity, connection, ErrBackpressure)
	}
	return err
}

func (r *Registry) WaitReady(ctx context.Context, identity Identity) error {
	for {
		r.mu.RLock()
		connection := r.connections[identity]
		changed := r.changed
		authorized := r.auth.Contains(identity)
		var terminalErr error
		var fenced bool
		if connection != nil {
			terminalErr, fenced = connection.terminalStatus()
		}
		r.mu.RUnlock()
		if !authorized {
			return ErrUnauthenticated
		}
		if fenced {
			if terminalErr != nil {
				return terminalErr
			}
			return ErrNotConnected
		}
		if connection == nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-changed:
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-connection.Ready:
			r.mu.RLock()
			active := r.connections[identity] == connection
			authorized = r.auth.Contains(identity)
			terminalErr, fenced = connection.terminalStatus()
			r.mu.RUnlock()
			if !authorized {
				return ErrUnauthenticated
			}
			if fenced {
				if terminalErr != nil {
					return terminalErr
				}
				return ErrNotConnected
			}
			if active {
				return nil
			}
			return ErrNotConnected
		case <-connection.done:
			if err := connection.terminalError(); err != nil {
				return err
			}
			return ErrNotConnected
		}
	}
}

func (r *Registry) attach(identity Identity) (*Connection, error) {
	if err := identity.validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.auth.Contains(identity) {
		return nil, ErrUnauthenticated
	}
	if _, exists := r.connections[identity]; exists {
		return nil, ErrAlreadyConnected
	}
	connection := newConnection(identity)
	r.connections[identity] = connection
	r.notifyChangedLocked()
	return connection, nil
}

func (r *Registry) detach(identity Identity, connection *Connection) {
	r.fenceAndWait(identity, connection, context.Canceled)
}

func (r *Registry) fenceAndWait(identity Identity, connection *Connection, err error) {
	r.publishFence(identity, connection, err)
	r.startFinalizer(identity, connection)
	<-connection.released
}

func (r *Registry) fenceForStreamExit(identity Identity, connection *Connection, err error) {
	r.publishFence(identity, connection, err)
	r.startFinalizer(identity, connection)
	if connection.drained() {
		<-connection.released
	}
}

func (r *Registry) publishFence(identity Identity, connection *Connection, err error) {
	r.mu.Lock()
	connection.beginFence(err)
	r.mu.Unlock()
}

func (r *Registry) startFinalizer(identity Identity, connection *Connection) {
	connection.finalizeOnce.Do(func() {
		go func() {
			connection.waitForDrain()
			r.mu.Lock()
			if r.connections[identity] == connection {
				delete(r.connections, identity)
				r.notifyChangedLocked()
			}
			r.mu.Unlock()
			close(connection.released)
		}()
	})
}

func (r *Registry) admit(identity Identity, connection *Connection) (*connectionOperation, error) {
	r.mu.RLock()
	active := r.connections[identity]
	authorized := r.auth.Contains(identity)
	if active != connection || !authorized {
		r.mu.RUnlock()
		if err := connection.terminalError(); err != nil {
			return nil, err
		}
		if !authorized {
			return nil, ErrUnauthenticated
		}
		return nil, ErrNotConnected
	}
	operation, err := connection.admit()
	r.mu.RUnlock()
	return operation, err
}

func (r *Registry) connectionCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.connections)
}

func (r *Registry) notifyChangedLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}
