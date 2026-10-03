package llm

import (
	"errors"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// PromptTooLong tells whether the API rejected a request for not fitting
// the context window, which a summary of the session can fix. The check is
// on the SDK's error as it comes: the estimate of the context the agent
// goes by may say it fits, and only the API counts for sure.
func PromptTooLong(err error) bool {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		if ae.StatusCode != http.StatusBadRequest {
			return false
		}
		msg := strings.ToLower(bodyError([]byte(ae.RawJSON())).Message)
		// The second is what models before model_context_window_exceeded
		// say when the request and max_tokens add up past the window.
		return strings.Contains(msg, "prompt is too long") || strings.Contains(msg, "exceed context limit")
	}
	var oe *openai.Error
	if errors.As(err, &oe) {
		return windowCode(apiError{Type: oe.Type, Code: oe.Code, Message: oe.Message})
	}
	var se *ssestream.StreamError
	if errors.As(err, &se) {
		return windowCode(bodyError(se.Event.Data))
	}
	var ee *APIError
	if errors.As(err, &ee) {
		return windowCode(apiError{Code: ee.Code, Message: ee.Message})
	}
	return false
}

// windowCode tells whether an error object of OpenAI's APIs is about the
// context window. Servers that speak them without the code (vLLM) keep
// OpenAI's message.
func windowCode(e apiError) bool {
	return e.Code == "context_length_exceeded" || strings.Contains(strings.ToLower(e.Message), "maximum context length")
}
