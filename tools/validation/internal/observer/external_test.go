package observer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExternalProbeProtocol(t *testing.T) {
	t.Setenv("SWITCHBOARD_OBSERVER_TEST_SECRET", "should-not-be-inherited")
	probe := filepath.Join(t.TempDir(), "probe")
	script := `#!/bin/sh
[ "${SWITCHBOARD_OBSERVER_TEST_SECRET+set}" != set ] || exit 4
[ "$1" = "https://fixture.test:8443/" ] || exit 4
case "$2" in
	present) exit 0 ;;
	absent) exit 3 ;;
	other) exit 255 ;;
esac
exit 4
`
	if err := os.WriteFile(probe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	const target = "https://fixture.test:8443/"
	for expected, want := range map[string]Outcome{"present": Present, "absent": Absent, "other": Inconclusive} {
		if got := External(context.Background(), probe, target, expected); got != want {
			t.Errorf("%s outcome = %d, want %d", expected, got, want)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := External(canceled, probe, target, "absent"); got != Inconclusive {
		t.Errorf("canceled observer outcome = %d, want inconclusive", got)
	}
	if got := External(context.Background(), probe+"-missing", target, "absent"); got != Inconclusive {
		t.Errorf("missing observer outcome = %d, want inconclusive", got)
	}
}
