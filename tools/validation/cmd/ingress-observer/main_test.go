package main

import (
	"context"
	"strings"
	"testing"

	"github.com/goliatone/switchboard-hub/tools/validation/internal/observer"
)

func TestReadOnlyProbeRejectsInvalidStdin(t *testing.T) {
	for _, input := range []string{
		"",
		"https://example.test/\n",
		"https://example.test/\nfixture-a\nextra\n",
		"https://example.test/\n" + strings.Repeat("x", 5000) + "\n",
		"http://example.test/\nfixture-a\n",
	} {
		if got := run(context.Background(), []string{"--stdin"}, strings.NewReader(input)); got != observer.Inconclusive {
			t.Errorf("invalid stdin outcome = %d, want inconclusive", got)
		}
	}
}
