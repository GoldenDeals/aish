package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// Short is err in one line for the terminal and the journal. An SDK's
// Error carries the URL, the request ID and the raw body of the reply; of
// all that only the API's own type and message say what went wrong.
func Short(err error) string {
	if err == nil {
		return ""
	}
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		if s := bodyError([]byte(ae.RawJSON())).String(); s != "" {
			return s
		}
		return statusText(ae.StatusCode)
	}
	var oe *openai.Error
	if errors.As(err, &oe) {
		// The SDK has already taken the error object out of the body.
		if s := (apiError{Type: oe.Type, Code: oe.Code, Message: oe.Message}).String(); s != "" {
			return s
		}
		return statusText(oe.StatusCode)
	}
	var se *ssestream.StreamError
	if errors.As(err, &se) {
		if s := bodyError(se.Event.Data).String(); s != "" {
			return s
		}
	}
	return firstLine(err.Error())
}

// apiError is the error object an API puts in a reply's body or in a
// stream's event: {"error": {"type": …, "code": …, "message": …}}.
type apiError struct {
	Type, Code, Message string
}

// bodyError reads the error object out of data. Servers that speak
// OpenAI's API differ in it: a code may be a number (an HTTP status), the
// whole error a string.
func bodyError(data []byte) apiError {
	var body struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return apiError{}
	}
	switch e := body.Error.(type) {
	case string:
		return apiError{Message: e}
	case map[string]any:
		return apiError{Type: scalar(e["type"]), Code: scalar(e["code"]), Message: scalar(e["message"])}
	}
	return apiError{}
}

func scalar(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

// String is "code: message", with the type in place of a code there is
// none of (Anthropic gives none, OpenAI not always).
func (e apiError) String() string {
	kind := e.Code
	if kind == "" {
		kind = e.Type
	}
	msg := firstLine(e.Message)
	switch {
	case kind == "":
		return msg
	case msg == "":
		return kind
	}
	return kind + ": " + msg
}

func statusText(code int) string {
	return strings.TrimSpace(fmt.Sprintf("%d %s", code, http.StatusText(code)))
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
