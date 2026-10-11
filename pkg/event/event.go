package event

import impl "github.com/GoldenDeals/aish-core/internal/core/event"

// Any is what the core routes on; every Event[T] and Stream[T] satisfies it.
type Any interface {
	Name() string
}

type Event[T any] interface {
	Any
	Data() T
}

type Filter func(Any) bool

func ByName(name string) Filter {
	return func(e Any) bool { return name == e.Name() }
}

// New returns an immutable event carrying data.
func New[T any](name string, data T) Event[T] { return impl.NewEvent(name, data) }
