package httputil

import "testing"

func TestFormatErrorPrefersStructuredFields(t *testing.T) {
	got := FormatError(400, []byte(`{"message":"bad request","detail":"ignored"}`))
	if got != "bad request" {
		t.Fatalf("FormatError() = %q, want %q", got, "bad request")
	}
}

func TestFormatErrorJoinsErrorsArray(t *testing.T) {
	got := FormatError(422, []byte(`{"errors":["one"," two "]}`))
	if got != "one; two" {
		t.Fatalf("FormatError() = %q, want %q", got, "one; two")
	}
}

func TestFormatErrorFallsBackToStatusCode(t *testing.T) {
	got := FormatError(503, []byte("   "))
	if got != "HTTP 503" {
		t.Fatalf("FormatError() = %q, want %q", got, "HTTP 503")
	}
}
