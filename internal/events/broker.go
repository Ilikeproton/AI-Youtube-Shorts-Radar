package events

import (
	"sync"

	"youtubeshort/internal/model"
)

type Broker struct {
	mu          sync.RWMutex
	subscribers map[int64]map[chan model.JobEvent]struct{}
}

func NewBroker() *Broker {
	return &Broker{
		subscribers: map[int64]map[chan model.JobEvent]struct{}{},
	}
}

func (b *Broker) Subscribe(batchID int64) (<-chan model.JobEvent, func()) {
	ch := make(chan model.JobEvent, 32)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.subscribers[batchID] == nil {
		b.subscribers[batchID] = map[chan model.JobEvent]struct{}{}
	}
	b.subscribers[batchID][ch] = struct{}{}

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if subs, ok := b.subscribers[batchID]; ok {
			delete(subs, ch)
			if len(subs) == 0 {
				delete(b.subscribers, batchID)
			}
		}
		close(ch)
	}

	return ch, cancel
}

func (b *Broker) Publish(batchID int64, event model.JobEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.subscribers[batchID] {
		select {
		case ch <- event:
		default:
		}
	}
}
