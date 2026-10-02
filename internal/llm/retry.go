package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
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
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func retryableStatus(code int) bool {
	switch code {
	case 408, 409, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}
