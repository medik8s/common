package nodeidentity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

type interruptedBody struct{}

func (interruptedBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

func TestBodyReadPreservesDeadline(t *testing.T) {
	_, err := decodeNodeBody(interruptedBody{})
	if !errors.Is(err, ErrAPIServer) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("body error must preserve both API classification and deadline: %v", err)
	}
}

func TestValidationResponseReadPreservesDeadline(t *testing.T) {
	err := classify(&http.Response{StatusCode: http.StatusUnprocessableEntity, Body: io.NopCloser(interruptedBody{})})
	if !errors.Is(err, ErrAPIServer) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("HTTP validation response must preserve body-read cause: %v", err)
	}
}
