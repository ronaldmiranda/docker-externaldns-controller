package docker

import (
	"context"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
)

// Watch emits a signal on the returned channel (coalesced with debounce)
// whenever a container lifecycle event may have changed the desired endpoints.
// It reconnects automatically if the event stream drops, and closes the channel
// when ctx is cancelled.
func (s *Source) Watch(ctx context.Context, debounce time.Duration) <-chan struct{} {
	out := make(chan struct{}, 1)

	f := make(client.Filters).
		Add("type", string(events.ContainerEventType)).
		Add("event", "start", "die", "stop", "kill", "destroy", "pause", "unpause")

	notify := func() {
		select {
		case out <- struct{}{}:
		default: // a reconcile is already pending
		}
	}

	go func() {
		defer close(out)
		var timer *time.Timer
		for ctx.Err() == nil {
			res := s.cli.Events(ctx, client.EventsListOptions{Filters: f})
			msgs, errs := res.Messages, res.Err
		stream:
			for {
				select {
				case <-ctx.Done():
					return
				case <-msgs:
					if timer != nil {
						timer.Stop()
					}
					timer = time.AfterFunc(debounce, notify)
				case err := <-errs:
					if ctx.Err() != nil {
						return
					}
					s.log.Warn("docker event stream error, reconnecting", "error", err)
					break stream
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()

	return out
}
