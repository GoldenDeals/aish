package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"syscall"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// Retryable tells whether a failed Complete may succeed if sent again: the
// API was overloaded or failed on its side, or the connection broke. A
// rejected request (400, 401, 403, 404, 413) is not.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		// An error event in the middle of a stream comes with the status
		// the stream began with, 200: only its type tells what went wrong.
		switch ae.Type() {
		case anthropic.ErrorTypeOverloadedError, anthropic.ErrorTypeAPIError,
			anthropic.ErrorTypeRateLimitError, anthropic.ErrorTypeTimeoutError:
			return true
		}
		return retryableStatus(ae.StatusCode)
	}
	var oe *openai.Error
	if errors.As(err, &oe) {
		return retryableStatus(oe.StatusCode)
	}
	// OpenAI's error in the middle of a stream is an event, not a status.
	var se *ssestream.StreamError
	if errors.As(err, &se) {
		return retryableKind(bodyError(se.Event.Data))
	}
	var ee *APIError
	if errors.As(err, &ee) {
		return retryableKind(apiError{Code: ee.Code})
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// APIError is a failure an API reports in an event of a stream of its own,
// one the SDK does not take for an error: the Responses API's error and
// response.failed. Code is the API's ("server_error"), what Retryable goes
// by.
type APIError struct {
	Code, Message string
}

// Error is what Short makes of an error object, "code: message".
func (e *APIError) Error() string {
	if s := (apiError{Code: e.Code, Message: e.Message}).String(); s != "" {
		return s
	}
	return "the API reported an error"
}

// retryableKind tells by its code, or else its type, whether an error an
// API reported in a stream is worth a retry.
func retryableKind(e apiError) bool {
	if code, err := strconv.Atoi(e.Code); err == nil {
		return retryableStatus(code)
	}
	return retryableKinds[e.Type] || retryableKinds[e.Code]
}

// retryableKinds are the types and codes of a stream's error event that
// mean the API failed on its side or is busy, as OpenAI and the servers
// that speak its API name them.
var retryableKinds = map[string]bool{
	"server_error": true, "api_error": true, "service_unavailable": true,
	"overloaded": true, "overloaded_error": true,
	"rate_limit_exceeded": true, "rate_limit_error": true,
	"timeout": true, "timeout_error": true,
}

func retryableStatus(code int) bool {
	switch code {
	case 408, 409, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}
