package event

import (
	"context"

	impl "github.com/GoldenDeals/aish-core/internal/core/event"
)

// Stream is an event whose data is still arriving from the sender's channel.
// Closing that channel finalizes the stream.
type Stream[T any] interface {
	// Data returns a copy of the chunks received so far: all of them once Finalized.
	Event[[]T]
	// Read returns a new copy of the stream from the start; it is closed once the stream is finalized.
	// ctx stops the copy, so a reader that gives up doesn't leave a goroutine behind.
	Read(ctx context.Context) <-chan T
	// Finalized reports whether the sender's channel is closed and fully consumed.
	Finalized() bool
	// Wait blocks until the stream is finalized or ctx ends. It doesn't depend on T, so the core uses it before commit.
	Wait(ctx context.Context) error
}

// NewStream starts consuming src. Write to src from a goroutine started before Emit or Dispatch
// and close it when done: the tx doesn't commit until then, and a sync subscriber reading
// the stream inline would otherwise wait forever.
func NewStream[T any](name string, src <-chan T) Stream[T] { return impl.NewStream(name, src) }

// Finalized reports whether ev is complete: plain events always are, streams once their channel is closed.
func Finalized(ev Any) bool {
	s, ok := ev.(interface{ Finalized() bool })
	return !ok || s.Finalized()
}
