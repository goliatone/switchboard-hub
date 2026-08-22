package diag

import (
	"errors"
	"strings"
	"testing"
)

func TestRedactRemovesCommonCredentialForms(t *testing.T) {
	in := "token=abc password = hunter2 Authorization: Bearer abc.def-123"
	got := Redact(in)
	for _, secret := range []string{"abc", "hunter2", "abc.def-123"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Redact(%q) leaked %q in %q", in, secret, got)
		}
	}
}

func TestSanitizeErrorHandlesNilAndRedacts(t *testing.T) {
	if SanitizeError(nil) != nil {
		t.Fatal("SanitizeError(nil) should return nil")
	}
	got := SanitizeError(errors.New("api_key=secret-value"))
	if got == nil || strings.Contains(got.Error(), "secret-value") {
		t.Fatalf("SanitizeError leaked credential: %v", got)
	}
}

func TestSetDebug(t *testing.T) {
	original := Enabled()
	t.Cleanup(func() { SetDebug(original) })
	SetDebug(true)
	if !Enabled() {
		t.Fatal("debug should be enabled")
	}
	SetDebug(false)
	if Enabled() {
		t.Fatal("debug should be disabled")
	}
}
