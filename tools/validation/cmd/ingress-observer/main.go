// ingress-observer implements the T08 external HTTPS probe protocol.
package main

import (
	"bufio"
	"context"
	"io"
	"os"

	"github.com/goliatone/switchboard-hub/tools/validation/internal/observer"
)

func main() {
	os.Exit(int(run(context.Background(), os.Args[1:], os.Stdin)))
}

func run(ctx context.Context, args []string, input io.Reader) observer.Outcome {
	switch {
	case len(args) == 1 && args[0] == "--stdin":
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 1024), 4096)
		if !scanner.Scan() {
			return observer.Inconclusive
		}
		target := scanner.Text()
		if !scanner.Scan() {
			return observer.Inconclusive
		}
		expected := scanner.Text()
		if scanner.Scan() || scanner.Err() != nil {
			return observer.Inconclusive
		}
		return observer.Check(ctx, target, expected)
	case len(args) == 2:
		return observer.Check(ctx, args[0], args[1])
	default:
		return observer.Inconclusive
	}
}
