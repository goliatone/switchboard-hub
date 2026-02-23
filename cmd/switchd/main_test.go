package main

import (
	"flag"
	"reflect"
	"testing"
)

func TestParseInterspersedFlags_PositionalThenFlag(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	port := fs.Int("port", 0, "")
	host := fs.String("host", "", "")

	pos, err := parseInterspersedFlags(fs, []string{"my-local-app", "--port", "3030"})
	if err != nil {
		t.Fatalf("parseInterspersedFlags returned error: %v", err)
	}
	if *port != 3030 {
		t.Fatalf("expected port 3030, got %d", *port)
	}
	if *host != "" {
		t.Fatalf("expected empty host, got %q", *host)
	}
	if len(pos) != 1 || pos[0] != "my-local-app" {
		t.Fatalf("unexpected positional args: %#v", pos)
	}
}

func TestParseInterspersedFlags_FlagThenPositional(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	port := fs.Int("port", 0, "")
	host := fs.String("host", "", "")

	pos, err := parseInterspersedFlags(fs, []string{"--port", "3030", "my-local-app"})
	if err != nil {
		t.Fatalf("parseInterspersedFlags returned error: %v", err)
	}
	if *port != 3030 {
		t.Fatalf("expected port 3030, got %d", *port)
	}
	if *host != "" {
		t.Fatalf("expected empty host, got %q", *host)
	}
	if len(pos) != 1 || pos[0] != "my-local-app" {
		t.Fatalf("unexpected positional args: %#v", pos)
	}
}

func TestParseInterspersedFlags_UnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	_ = fs.Int("port", 0, "")

	if _, err := parseInterspersedFlags(fs, []string{"--nope", "x"}); err == nil {
		t.Fatal("expected unknown flag error")
	}
}

func TestExtractGlobalFlags(t *testing.T) {
	args, opts, err := extractGlobalFlags([]string{"--debug", "app", "ls"})
	if err != nil {
		t.Fatalf("extractGlobalFlags returned error: %v", err)
	}
	if !opts.Debug {
		t.Fatal("expected debug=true")
	}
	want := []string{"app", "ls"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected args: got=%v want=%v", args, want)
	}
}
