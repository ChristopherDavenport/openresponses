package openresponses

import (
	"context"
	"fmt"
	"sync"
)

// ResponseStore remembers the conversation history of completed responses
// keyed by response ID, so a later request may continue with
// previous_response_id and only new input. History is the request's
// input followed by the response's output. The handler hands Save a copy
// it will not touch again and copies what Load returns before an adapter
// sees it, so an implementation may keep the slices it is given.
//
// Give the [Handler] a store with [WithResponseStore] and it resolves
// previous_response_id itself over HTTP, on the compaction endpoint and
// over WebSocket, exactly the way the WebSocket connection cache does:
// the history is inlined into the request input and the field is
// cleared before the adapter runs. Responses with store:false are never
// saved. A single replica can use [MemoryStore]; a fleet supplies a
// shared implementation.
type ResponseStore interface {
	// Load returns the history for id, or ok=false when it is unknown.
	Load(ctx context.Context, id string) (history Items, ok bool, err error)
	// Save records the history for id, replacing any previous entry.
	Save(ctx context.Context, id string, history Items) error
	// Delete forgets id. Deleting an unknown id is not an error.
	Delete(ctx context.Context, id string) error
}

// MemoryStore is an in-memory [ResponseStore] that keeps the most recent
// entries up to a fixed capacity. It is safe for concurrent use.
type MemoryStore struct {
	mu       sync.Mutex
	capacity int
	order    []string
	items    map[string]Items
}

var _ ResponseStore = (*MemoryStore)(nil)

// NewMemoryStore returns a store that keeps at most capacity entries.
// A capacity below one is treated as one.
func NewMemoryStore(capacity int) *MemoryStore {
	if capacity < 1 {
		capacity = 1
	}
	return &MemoryStore{capacity: capacity, items: make(map[string]Items, capacity)}
}

// Load implements ResponseStore.
func (m *MemoryStore) Load(_ context.Context, id string) (Items, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, ok := m.items[id]
	return items, ok, nil
}

// Save implements ResponseStore, evicting the oldest entries beyond the
// capacity.
func (m *MemoryStore) Save(_ context.Context, id string, history Items) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.items[id]; !exists {
		m.order = append(m.order, id)
	}
	m.items[id] = history
	for len(m.order) > m.capacity {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.items, oldest)
	}
	return nil
}

// Delete implements ResponseStore.
func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.items[id]; !exists {
		return nil
	}
	delete(m.items, id)
	for i, v := range m.order {
		if v == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return nil
}

// Len returns the number of stored entries.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// continuation is the outcome of resolving a request's
// previous_response_id.
type continuation struct {
	// id is the previous_response_id the client sent, or "".
	id string
	// resolved reports that history was found and inlined.
	resolved bool
	// source is the store that answered, for eviction on failure.
	source ResponseStore
}

// resolveContinuation looks req.PreviousResponseID up in stores in order.
// On a hit a copy of the history is inlined ahead of req.Input, function
// call outputs are checked against it, and the field is cleared so the
// adapter sees a self-contained request. The copy means an adapter that
// mutates its input never reaches into the store. A miss leaves req
// untouched; the caller decides whether that is an error.
func resolveContinuation(ctx context.Context, req *Request, stores ...ResponseStore) (continuation, error) {
	c := continuation{id: req.PreviousResponseID}
	if c.id == "" {
		return c, nil
	}
	for _, store := range stores {
		if store == nil {
			continue
		}
		history, ok, err := store.Load(ctx, c.id)
		if err != nil {
			return c, fmt.Errorf("load previous response %q: %w", c.id, err)
		}
		if !ok {
			continue
		}
		merged := make(Items, 0, len(history)+len(req.Input))
		merged = append(merged, history.Clone()...)
		merged = append(merged, req.Input...)
		c.resolved = true
		c.source = store
		if err := checkFunctionCallOutputs(merged); err != nil {
			return c, err
		}
		req.Input = merged
		req.PreviousResponseID = ""
		return c, nil
	}
	return c, nil
}

// history returns the conversation to remember for a completed response:
// a copy, so the store never shares items with the adapter that produced
// them.
func history(req Request, resp *Response) Items {
	out := make(Items, 0, len(req.Input)+len(resp.Output))
	out = append(out, req.Input...)
	out = append(out, resp.Output...)
	return out.Clone()
}

// stampPreviousID restores previous_response_id on response snapshots
// when the server resolved the continuation itself and cleared the
// field before the adapter ran.
func stampPreviousID(ev StreamEvent, id string) {
	if id == "" {
		return
	}
	var resp *Response
	switch e := ev.(type) {
	case *ResponseCreatedEvent:
		resp = e.Response
	case *ResponseQueuedEvent:
		resp = e.Response
	case *ResponseInProgressEvent:
		resp = e.Response
	case *ResponseCompletedEvent:
		resp = e.Response
	case *ResponseFailedEvent:
		resp = e.Response
	case *ResponseIncompleteEvent:
		resp = e.Response
	}
	if resp != nil && resp.PreviousResponseID == nil {
		resp.PreviousResponseID = &id
	}
}
