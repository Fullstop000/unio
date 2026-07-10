package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorString(t *testing.T) {
	if got := Timeout("").Error(); got != "timeout" {
		t.Fatalf("empty msg should render kind only, got %q", got)
	}
	if got := Transport("eof").Error(); got != "transport: eof" {
		t.Fatalf("unexpected string %q", got)
	}
	var nilErr *AgentError
	if got := nilErr.Error(); got != "<nil>" {
		t.Fatalf("nil receiver should be safe, got %q", got)
	}
}

func TestErrorKindValid(t *testing.T) {
	for _, k := range []ErrorKind{KindTransport, KindProtocol, KindTimeout, KindRuntimeReported, KindUnsupported} {
		if !k.Valid() {
			t.Fatalf("%q should be valid", k)
		}
	}
	if ErrorKind("bogus").Valid() {
		t.Fatal("unknown kind should be invalid")
	}
}

func TestErrorsIsByCategory(t *testing.T) {
	err := Protocol("bad frame")
	// errors.Is against another AgentError of the same kind matches by category.
	if !errors.Is(err, Protocol("different message")) {
		t.Fatal("errors.Is should match by Kind regardless of message")
	}
	if errors.Is(err, Timeout("x")) {
		t.Fatal("errors.Is should not match a different Kind")
	}
}

func TestErrorsAsAndKindOf(t *testing.T) {
	wrapped := fmt.Errorf("context: %w", RuntimeReported("boom"))

	var ae *AgentError
	if !errors.As(wrapped, &ae) {
		t.Fatal("errors.As should extract the AgentError through fmt wrapping")
	}
	if ae.Kind != KindRuntimeReported {
		t.Fatalf("unexpected kind %q", ae.Kind)
	}

	kind, ok := KindOf(wrapped)
	if !ok || kind != KindRuntimeReported {
		t.Fatalf("KindOf should recover the category, got %q ok=%v", kind, ok)
	}

	if _, ok := KindOf(errors.New("plain")); ok {
		t.Fatal("KindOf should report false for a non-AgentError")
	}
}

func TestUnwrap(t *testing.T) {
	cause := errors.New("root cause")
	err := Wrap(KindTransport, "pipe closed", cause)
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should reach the wrapped cause via Unwrap")
	}
}

func TestInvalidStateAndSessionNotFoundKinds(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind ErrorKind
	}{
		{InvalidState("busy"), KindInvalidState},
		{SessionNotFound("s-1"), KindSessionNotFound},
	} {
		got, ok := KindOf(tc.err)
		if !ok || got != tc.kind {
			t.Fatalf("KindOf(%v) = %q, %v; want %q, true", tc.err, got, ok, tc.kind)
		}
	}
}
