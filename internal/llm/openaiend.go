package llm

import (
	"bytes"
	"io"
	"net/http"

	"github.com/openai/openai-go/v3/option"
)

// streamEnd tells whether a Chat Completions stream came to [DONE]. The
// SDK's Stream ends alike on it and on a connection closed with nothing
// wrong to read, and a server that gives no finish reason has only that
// line to tell a whole reply from the start of one.
type streamEnd struct {
	done bool
	line []byte // the start of the line being read, no longer than doneLine
}

var doneLine = []byte("data: [DONE]")

// option has the reply's body read through e. The SDK's own retries each
// read a body of their own, and e tells of the last.
func (e *streamEnd) option() option.RequestOption {
	return option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		resp, err := next(req)
		if err == nil && resp != nil && resp.Body != nil {
			*e = streamEnd{}
			resp.Body = endBody{resp.Body, e}
		}
		return resp, err
	})
}

func (e *streamEnd) scan(p []byte, eof bool) {
	for _, c := range p {
		switch {
		case c == '\n' || c == '\r':
			e.endLine()
		case len(e.line) < len(doneLine):
			e.line = append(e.line, c)
		}
	}
	if eof {
		e.endLine()
	}
}

// endLine is the end of a line, as the SDK splits an event's into the
// field and its value: data with [DONE] at its start is the stream's end.
func (e *streamEnd) endLine() {
	v, data := bytes.CutPrefix(e.line, []byte("data:"))
	v = bytes.TrimPrefix(v, []byte(" "))
	e.done = e.done || data && bytes.HasPrefix(v, []byte("[DONE]"))
	e.line = e.line[:0]
}

type endBody struct {
	io.ReadCloser
	end *streamEnd
}

func (b endBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.end.scan(p[:n], err == io.EOF)
	return n, err
}
