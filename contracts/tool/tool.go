// Package tool is the bus contract between the modules that give the model tools and the LLM provider.
//
// A tool module declares its tool once, from Start, with a Declare event; two tools with one name are an error.
// The provider offers the declared tools to the model. The tool calls of an answer go out as Call events in
// the tx of that answer, after its llm.answer stream. Each Call gets exactly one Result: from the tool, or from
// whoever holds the call and doesn't let it run, a policy that denies it. Once every Call of the answer has
// its Result, the provider asks the model again, in the same turn: the turn ends with the answer that calls no tools.
// A Call's ctx carries the turn (llm.Turn): stop the work when it is done.
package tool

import "encoding/json"

const (
	Declare = "tool.declare" // event.Event[Spec]: a tool the model may call, for the rest of the session
	Call    = "tool.call"    // event.Event[Use]: the model calls a tool
	Result  = "tool.result"  // event.Event[Output]: the outcome of a Call
)

type Spec struct {
	Name        string
	Description string          // when and how to use the tool: the model reads only this and the schema
	Schema      json.RawMessage // JSON Schema of the input, an object
}

type Use struct {
	ID    string // unique in the session; the Output carries it back
	Name  string
	Input json.RawMessage // a JSON object, as the model wrote it: validate it
}

type Output struct {
	ID    string
	Text  string
	Error bool // the call failed; Text says why
}
