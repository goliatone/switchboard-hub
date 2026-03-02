package main

import (
	"testing"

	"github.com/goliatone/switchboard-hub/internal/config"
)

func TestGlobalFlagsUseJSON(t *testing.T) {
	cases := []struct {
		name string
		in   globalFlags
		want bool
	}{
		{name: "default text", in: globalFlags{Output: "text"}, want: false},
		{name: "explicit json flag", in: globalFlags{JSON: true, Output: "text"}, want: true},
		{name: "output json", in: globalFlags{Output: "json"}, want: true},
		{name: "output json uppercase", in: globalFlags{Output: "JSON"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.useJSON(); got != tc.want {
				t.Fatalf("useJSON()=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestFindAppByInput(t *testing.T) {
	apps := []config.App{
		{Name: "esign", LocalHost: "esign.test"},
		{Name: "api", LocalHost: "api.test"},
	}

	if got, ok := findAppByInput(apps, "esign"); !ok || got.Name != "esign" {
		t.Fatalf("expected name match for esign, got=%#v ok=%v", got, ok)
	}
	if got, ok := findAppByInput(apps, "https://api.test"); !ok || got.Name != "api" {
		t.Fatalf("expected host match for api.test, got=%#v ok=%v", got, ok)
	}
	if _, ok := findAppByInput(apps, "missing"); ok {
		t.Fatal("expected no match for missing app")
	}
}
