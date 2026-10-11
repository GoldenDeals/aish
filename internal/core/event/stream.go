package event

import (
	"context"
	"slices"
	"sync"
)

// NewStream is the implementation behind event.NewStream.
func NewStream[T any](name string, src <-chan T) *Stream[T] {
	s := &Stream[T]{name: name, wake: make(chan struct{})}
	go func() {
		for v := range src {
			s.update(func() { s.buf = append(s.buf, v) })
		}
		s.update(func() { s.done = true })
	}()
	return s
}

type Stream[T any] struct {
	name string

	mu   sync.Mutex
	buf  []T
	done bool
	wake chan struct{} // closed and replaced on every change to wake all readers
}

func (s *Stream[T]) update(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f()
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *Stream[T]) snapshot() ([]T, bool, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf, s.done, s.wake
}

func (s *Stream[T]) Name() string { return s.name }

func (s *Stream[T]) Data() []T {
	buf, _, _ := s.snapshot()
	return slices.Clone(buf)
}

func (s *Stream[T]) Finalized() bool {
	_, done, _ := s.snapshot()
	return done
}

func (s *Stream[T]) Read(ctx context.Context) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out)
		for i := 0; ; {
			buf, done, wake := s.snapshot()
			for ; i < len(buf); i++ {
				select {
				case out <- buf[i]:
				case <-ctx.Done():
					return
				}
			}
			if done {
				return
			}
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (s *Stream[T]) Wait(ctx context.Context) error {
	for range s.Read(ctx) {
	}
	if s.Finalized() {
		return nil
	}
	return ctx.Err()
}
