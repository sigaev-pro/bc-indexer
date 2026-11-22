// The in-memory Remote adapter: a configurable account and stream table
// with an availability switch. It stands in for the pinned nats.go client
// in tests and in the smoke corpus; the production adapter is wired at the
// gateway process boundary.
package destination

import (
	"context"
	"fmt"
	"sync"
)

// MemoryRemote is the in-memory Remote adapter.
type MemoryRemote struct {
	mu       sync.Mutex
	account  string
	streams  map[string]StreamInfo
	up       bool
	failNext int // when > 0, the next call answers unavailable
}

// NewMemoryRemote returns an available remote with the account and streams
// of a bootstrapped environment.
func NewMemoryRemote(account string, streams ...StreamInfo) (*MemoryRemote, error) {
	if account == "" {
		return nil, fmt.Errorf("%w: account is empty", ErrDescriptor)
	}
	r := &MemoryRemote{account: account, streams: make(map[string]StreamInfo, len(streams)), up: true}
	for _, s := range streams {
		if s.Name == "" {
			return nil, fmt.Errorf("%w: stream name is empty", ErrDescriptor)
		}
		r.streams[s.Name] = s
	}
	return r, nil
}

// SetAvailable switches the remote availability, modeling a NATS outage.
func (r *MemoryRemote) SetAvailable(up bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.up = up
}

// FailNextCall makes the next call answer unavailable once.
func (r *MemoryRemote) FailNextCall() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failNext = 1
}

// SetStream replaces one stream configuration.
func (r *MemoryRemote) SetStream(info StreamInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams[info.Name] = info
}

// AccountInfo returns the account, or ErrUnavailable when down.
func (r *MemoryRemote) AccountInfo(context.Context) (AccountInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkUp("account info"); err != nil {
		return AccountInfo{}, err
	}
	return AccountInfo{Account: r.account}, nil
}

// StreamInfo returns the stream configuration, or ErrUnavailable when down
// or the stream does not exist on the server.
func (r *MemoryRemote) StreamInfo(_ context.Context, stream string) (StreamInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.checkUp("stream info"); err != nil {
		return StreamInfo{}, err
	}
	info, ok := r.streams[stream]
	if !ok {
		return StreamInfo{}, fmt.Errorf("%w: stream %q not found", ErrUnavailable, stream)
	}
	return info, nil
}

// checkUp answers the availability switch under the lock.
func (r *MemoryRemote) checkUp(what string) error {
	if r.failNext > 0 {
		r.failNext--
		return fmt.Errorf("%w: %s request failed once", ErrUnavailable, what)
	}
	if !r.up {
		return fmt.Errorf("%w: %s has no connection", ErrUnavailable, what)
	}
	return nil
}
