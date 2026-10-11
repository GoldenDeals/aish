package event

import (
	"context"
	"slices"
	"testing"
)

type named string

func (n named) Name() string { return string(n) }

func collect[T any](ch <-chan T) []T {
	var out []T
	for v := range ch {
		out = append(out, v)
	}
	return out
}

func TestStream(t *testing.T) {
	ctx := context.Background()
	want := []string{"a", "b", "c"}
	src := make(chan string)
	s := NewStream("llm", src)
	early := s.Read(ctx)

	if Finalized(s) {
		t.Fatal("finalized before src is closed")
	}
	go func() {
		for _, v := range want {
			src <- v
		}
		close(src)
	}()

	if got := collect(early); !slices.Equal(got, want) {
		t.Fatalf("early reader got %v, want %v", got, want)
	}
	if err := s.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if !Finalized(s) {
		t.Fatal("not finalized after src is closed")
	}
	if got := collect(s.Read(ctx)); !slices.Equal(got, want) {
		t.Fatalf("late reader got %v, want %v", got, want)
	}
	var ev Event[[]string] = s
	got := ev.Data()
	if !slices.Equal(got, want) {
		t.Fatalf("Data got %v, want %v", got, want)
	}
	if got[0] = "x"; s.Data()[0] != "a" {
		t.Fatal("Data shares its buffer with the stream")
	}
	if !Finalized(named("plain")) {
		t.Fatal("plain event must be finalized")
	}
}

func TestStreamCancel(t *testing.T) {
	s := NewStream("llm", make(chan string)) // never closed
	ctx, cancel := context.WithCancel(context.Background())
	ch := s.Read(ctx)
	cancel()

	if _, ok := <-ch; ok {
		t.Fatal("reader not closed after cancel")
	}
	if err := s.Wait(ctx); err == nil {
		t.Fatal("Wait returned nil for an open stream")
	}
}
