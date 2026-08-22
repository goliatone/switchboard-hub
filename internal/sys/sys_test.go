package sys

import (
	"strings"
	"testing"
)

func TestRunCapture(t *testing.T) {
	out, err := RunCapture("go", "env", "GOOS")
	if err != nil {
		t.Fatalf("RunCapture returned error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("RunCapture returned empty GOOS")
	}
}

func TestRunCaptureReportsMissingCommand(t *testing.T) {
	const name = "switchboard-command-that-does-not-exist"
	if _, err := RunCapture(name); err == nil || !strings.Contains(err.Error(), name) {
		t.Fatalf("RunCapture missing command error = %v", err)
	}
}

func TestExists(t *testing.T) {
	if !Exists("go") {
		t.Fatal("go should exist in the test PATH")
	}
	if Exists("switchboard-command-that-does-not-exist") {
		t.Fatal("unexpected command found")
	}
}
