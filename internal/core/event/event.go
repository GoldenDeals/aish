package event

type EmitConfig struct{}

// Event is the implementation behind event.New.
type Event[T any] struct {
	name string
	data T
}

func NewEvent[T any](name string, data T) Event[T] { return Event[T]{name, data} }

func (e Event[T]) Name() string { return e.name }
func (e Event[T]) Data() T      { return e.data }
