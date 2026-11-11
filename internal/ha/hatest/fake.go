// Package hatest provides an in-memory ha.Client for tests.
package hatest

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	"github.com/igrybkov/ha-guest-access/internal/ha"
)

// ServiceCall is one recorded CallService call.
type ServiceCall struct {
	Domain, Service string
	Data            map[string]any
	Target          *ha.Target
	ReturnResponse  bool
}

// FiredEvent is one recorded FireEvent call.
type FiredEvent struct {
	EventType string
	Data      map[string]any
}

// Fake is an in-memory ha.Client. Set the registry fields before use; read
// Calls (or ServiceCalls, when calls run on other goroutines) to assert the
// service call sequence. Emit delivers an event to subscribers.
type Fake struct {
	AreaList   []ha.Area
	DeviceList []ha.Device
	EntityList []ha.Entity
	LabelList  []ha.Label
	StateList  []ha.State
	Core       ha.Config
	Calls      []ServiceCall
	Fired      []FiredEvent

	// Respond, if set, returns the response and error for a service call.
	// The call is recorded either way.
	Respond func(ServiceCall) (json.RawMessage, error)

	mu   sync.Mutex
	subs map[string][]*sub
}

type sub struct {
	ctx context.Context
	fn  func(ha.Event)
}

var _ ha.Client = (*Fake)(nil)

// Emit delivers ev to every live subscriber of ev.EventType, synchronously,
// in subscription order.
func (f *Fake) Emit(ev ha.Event) {
	f.mu.Lock()
	subs := slices.Clone(f.subs[ev.EventType])
	f.mu.Unlock()
	for _, s := range subs {
		if s.ctx.Err() == nil {
			s.fn(ev)
		}
	}
}

// EmitData is Emit with data marshaled to JSON.
func (f *Fake) EmitData(eventType string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	f.Emit(ha.Event{EventType: eventType, Data: raw})
}

// Subscribers returns the number of live subscribers of eventType.
func (f *Fake) Subscribers(eventType string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.subs[eventType] {
		if s.ctx.Err() == nil {
			n++
		}
	}
	return n
}

// ServiceCalls returns a copy of the recorded calls. Safe for concurrent use.
func (f *Fake) ServiceCalls() []ServiceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.Calls)
}

// Reset clears the recorded calls.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = nil
}

func (f *Fake) SubscribeEvents(ctx context.Context, eventType string, fn func(ha.Event)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs == nil {
		f.subs = make(map[string][]*sub)
	}
	f.subs[eventType] = append(f.subs[eventType], &sub{ctx: ctx, fn: fn})
	return nil
}

func (f *Fake) CallService(ctx context.Context, domain, service string, data map[string]any, target *ha.Target, returnResponse bool) (json.RawMessage, error) {
	call := ServiceCall{Domain: domain, Service: service, Data: data, Target: target, ReturnResponse: returnResponse}
	f.mu.Lock()
	f.Calls = append(f.Calls, call)
	respond := f.Respond
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if respond != nil {
		return respond(call)
	}
	return nil, nil
}

func (f *Fake) Areas(ctx context.Context) ([]ha.Area, error)      { return f.AreaList, nil }
func (f *Fake) Devices(ctx context.Context) ([]ha.Device, error)  { return f.DeviceList, nil }
func (f *Fake) Entities(ctx context.Context) ([]ha.Entity, error) { return f.EntityList, nil }
func (f *Fake) Labels(ctx context.Context) ([]ha.Label, error)    { return f.LabelList, nil }
func (f *Fake) Config(ctx context.Context) (ha.Config, error)     { return f.Core, nil }

// States returns a copy of StateList. Safe for concurrent use with
// SetState.
func (f *Fake) States(ctx context.Context) ([]ha.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.StateList), nil
}

// State returns the state of entityID, if StateList has one.
func (f *Fake) State(entityID string) (ha.State, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.stateIndex(entityID); i >= 0 {
		return f.StateList[i], true
	}
	return ha.State{}, false
}

// SetState puts st in StateList, in place of the entity's old state if
// it had one. Safe for concurrent use with States.
func (f *Fake) SetState(st ha.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.stateIndex(st.EntityID); i >= 0 {
		f.StateList[i] = st
		return
	}
	f.StateList = append(f.StateList, st)
}

func (f *Fake) stateIndex(entityID string) int {
	return slices.IndexFunc(f.StateList, func(s ha.State) bool { return s.EntityID == entityID })
}

// FireEvent records the event. Read it with FiredEvents.
func (f *Fake) FireEvent(ctx context.Context, eventType string, data map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Fired = append(f.Fired, FiredEvent{EventType: eventType, Data: data})
	return ctx.Err()
}

// FiredEvents returns a copy of the recorded events. Safe for concurrent use.
func (f *Fake) FiredEvents() []FiredEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.Fired)
}
