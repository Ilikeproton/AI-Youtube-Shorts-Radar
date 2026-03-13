package events

import (
	"testing"
	"time"

	"youtubeshort/internal/model"
)

func TestBrokerPublishesInOrder(t *testing.T) {
	t.Parallel()

	broker := NewBroker()
	ch, cancel := broker.Subscribe(7)
	defer cancel()

	for i := 1; i <= 3; i++ {
		broker.Publish(7, model.JobEvent{Sequence: i, CreatedAt: time.Now()})
	}

	for i := 1; i <= 3; i++ {
		event := <-ch
		if event.Sequence != i {
			t.Fatalf("expected sequence %d, got %d", i, event.Sequence)
		}
	}
}
