package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"strings"

	"github.com/inebotov/aish/internal/rpc"
	"github.com/inebotov/aish/internal/tools"
)

// Remote returns the MCP tools the proxy knows as tools that call it, and
// the problems it reported. Wait starts servers whose tools are unknown.
func Remote(c *rpc.Client, wait bool) ([]tools.Tool, []string) {
	timeout := rpc.CallTimeout
	if wait {
		timeout += startTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var res ListResult
	if err := c.CallContext(ctx, rpc.MethodMCPList, ListParams{Wait: wait}, &res); err != nil {
		return nil, []string{"mcp: " + err.Error()}
	}
	return convert(res, func(ctx context.Context, info ToolInfo, args map[string]any) (string, error) {
		return call(ctx, c, info, args)
	})
}

// Local returns the manager's tools for the agent in the manager's own
// process, the proxy, and the problems found.
func Local(ctx context.Context, m *Manager, wait bool) ([]tools.Tool, []string) {
	res := m.List(ctx, wait)
	return convert(res, func(ctx context.Context, info ToolInfo, args map[string]any) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, info.Timeout)
		defer cancel()
		raw, err := m.Call(ctx, info.Name, args)
		if err != nil {
			return "", err
		}
		return Format(raw)
	})
}

// convert makes tools of a listing, each run through run.
func convert(res ListResult, run func(context.Context, ToolInfo, map[string]any) (string, error)) ([]tools.Tool, []string) {
	var out []tools.Tool
	for _, info := range res.Tools {
		args, err := Args(info.Schema)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", info.Name, err))
			continue
		}
		out = append(out, remote{info: info, args: args, run: run})
	}
	return out, res.Errors
}

// remote is an MCP tool: a call goes to its server through the manager.
type remote struct {
	info ToolInfo
	args []tools.Arg
	run  func(context.Context, ToolInfo, map[string]any) (string, error)
}

func (t remote) Name() string      { return t.info.Name }
func (t remote) Desc() string      { return t.info.Description }
func (t remote) Args() []tools.Arg { return t.args }
func (t remote) Server() string    { return t.info.Server }
func (t remote) Hidden() bool      { return t.info.Expose != "tools" }

// Schema is the server's own, with what Args lose (enums, nesting); a tool
// without one takes no arguments.
func (t remote) Schema() map[string]any {
	var m map[string]any
	if len(t.info.Schema) > 0 && json.Unmarshal(t.info.Schema, &m) == nil {
		return m
	}
	return tools.Schema(t.args)
}

// Execute runs the tool in its server, whose directory and environment are
// its own.
func (t remote) Execute(ctx context.Context, _ tools.Exec, args map[string]any, _ io.Writer) (string, error) {
	return t.run(ctx, t.info, args)
}

func call(ctx context.Context, c *rpc.Client, info ToolInfo, args map[string]any) (string, error) {
	timeout := info.Timeout
	if timeout <= 0 { // a proxy older than the field
		timeout = startTimeout + callTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+rpc.CallTimeout)
	defer cancel()
	var raw json.RawMessage
	if err := c.CallContext(ctx, rpc.MethodMCPCall, CallParams{Name: info.Name, Args: args}, &raw); err != nil {
		return "", err
	}
	return Format(raw)
}

// Format turns a CallToolResult into command output: text as is, binary
// content saved to a temporary file whose path is printed. A result the
// server marks as an error is an error, so the command exits non-zero.
func Format(raw json.RawMessage) (string, error) {
	var r struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
			URI      string `json:"uri"`
			Resource *struct {
				URI      string `json:"uri"`
				Text     string `json:"text"`
				Blob     string `json:"blob"`
				MimeType string `json:"mimeType"`
			} `json:"resource"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("bad tool result: %w", err)
	}
	var parts []string
	for _, c := range r.Content {
		switch {
		case c.Type == "text":
			parts = append(parts, c.Text)
		case c.Data != "":
			parts = append(parts, saveBlob(c.Data, c.MimeType))
		case c.Resource != nil && c.Resource.Blob != "":
			parts = append(parts, saveBlob(c.Resource.Blob, c.Resource.MimeType))
		case c.Resource != nil:
			parts = append(parts, c.Resource.Text)
		case c.URI != "":
			parts = append(parts, c.URI)
		}
	}
	if len(parts) == 0 && len(r.Structured) > 0 {
		var b bytes.Buffer
		json.Indent(&b, r.Structured, "", "  ")
		parts = append(parts, b.String())
	}
	out := strings.Join(parts, "\n")
	if r.IsError {
		return out, errors.New("the tool reported an error")
	}
	return out, nil
}

func saveBlob(data, mimeType string) string {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "[undecodable " + mimeType + " content]"
	}
	ext := ""
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		ext = exts[len(exts)-1]
	}
	f, err := os.CreateTemp("", "aish-mcp-*"+ext)
	if err == nil {
		_, err = f.Write(b)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(f.Name())
		}
	}
	if err != nil {
		return "[" + mimeType + " content not saved: " + err.Error() + "]"
	}
	return f.Name()
}

// Args derives the CLI form from an input schema: required scalars are
// positional, in the schema's order; everything else is a --flag, with
// objects and arrays given as JSON.
func Args(schema json.RawMessage) ([]tools.Arg, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	var s struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("bad input schema: %w", err)
	}
	names, err := keys(s.Properties)
	if err != nil {
		return nil, fmt.Errorf("bad input schema: %w", err)
	}
	var props map[string]struct {
		Type        json.RawMessage `json:"type"`
		Description string          `json:"description"`
	}
	json.Unmarshal(s.Properties, &props)
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	var args []tools.Arg
	for _, n := range names {
		p := props[n]
		a := tools.Arg{Name: n, Type: typeOf(p.Type), Desc: p.Description, Required: required[n]}
		switch a.Type {
		case "string", "integer", "number", "boolean":
			a.Flag = !a.Required
		default:
			a.Flag = true
		}
		args = append(args, a)
	}
	return args, nil
}

// keys lists an object's keys in their order in the JSON text, which a map
// would lose.
func keys(obj json.RawMessage) ([]string, error) {
	if len(obj) == 0 {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(obj))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("properties is not an object")
	}
	var out []string
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		out = append(out, t.(string))
		var skip json.RawMessage
		if err := d.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// typeOf reads a schema "type", which may be a list such as ["string", "null"].
func typeOf(raw json.RawMessage) string {
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" && one != "null" {
		return one
	}
	var many []string
	json.Unmarshal(raw, &many)
	for _, t := range many {
		if t != "null" {
			return t
		}
	}
	return "any"
}
