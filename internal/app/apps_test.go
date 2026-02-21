package app

import (
	"testing"

	"github.com/goliatone/switchboard-hub/internal/config"
)

func TestUpsertAppWritesLegacyRoute(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	got, err := upsertApp(c, "esign", 3000)
	if err != nil {
		t.Fatalf("upsertApp returned error: %v", err)
	}

	if got.Name != "esign" {
		t.Fatalf("unexpected app name: %q", got.Name)
	}
	if got.LocalHost != "esign.test" {
		t.Fatalf("unexpected local host: %q", got.LocalHost)
	}
	if len(c.Routes) != 1 {
		t.Fatalf("expected 1 legacy route, got %d", len(c.Routes))
	}
	if c.Routes[0].Host != "esign.test" || c.Routes[0].Dial != "127.0.0.1:3000" {
		t.Fatalf("unexpected legacy route: %#v", c.Routes[0])
	}
}

func TestUpsertAppRejectsDuplicateName(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	if _, err := upsertApp(c, "esign", 3000); err != nil {
		t.Fatalf("upsertApp returned error: %v", err)
	}
	if _, err := upsertApp(c, "esign", 3001); err == nil {
		t.Fatal("expected duplicate app name error")
	}
}

func TestUpsertAppRejectsDuplicateHost(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	if _, err := upsertApp(c, "esign.test", 3000); err != nil {
		t.Fatalf("upsertApp returned error: %v", err)
	}
	if _, err := upsertApp(c, "esign.test", 3001); err == nil {
		t.Fatal("expected duplicate app host error")
	}
}

func TestValidateAppPort(t *testing.T) {
	bad := []int{0, -1, 65536}
	for _, p := range bad {
		if err := validateAppPort(p); err == nil {
			t.Fatalf("expected invalid port error for %d", p)
		}
	}
	if err := validateAppPort(3000); err != nil {
		t.Fatalf("validateAppPort returned error: %v", err)
	}
}

func TestNormalizeAppNameInput(t *testing.T) {
	got, err := normalizeAppNameInput("Esign_App")
	if err != nil {
		t.Fatalf("normalizeAppNameInput returned error: %v", err)
	}
	if got != "esign-app" {
		t.Fatalf("unexpected normalized name: %q", got)
	}
}

func TestSyncAppFromRouteUpdatesExistingHost(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	if _, err := upsertApp(c, "esign", 3000); err != nil {
		t.Fatalf("upsertApp returned error: %v", err)
	}
	if err := syncAppFromRoute(c, "esign.test", 4000); err != nil {
		t.Fatalf("syncAppFromRoute returned error: %v", err)
	}
	if len(c.Apps) != 1 {
		t.Fatalf("expected one app, got %d", len(c.Apps))
	}
	if c.Apps[0].LocalPort != 4000 {
		t.Fatalf("expected updated port 4000, got %d", c.Apps[0].LocalPort)
	}
}

func TestRemoveAppByHost(t *testing.T) {
	c := config.Default("test", "10.0.0.1")
	if _, err := upsertApp(c, "esign", 3000); err != nil {
		t.Fatalf("upsertApp returned error: %v", err)
	}
	removeAppByHost(c, "esign.test")
	if len(c.Apps) != 0 {
		t.Fatalf("expected no apps, got %d", len(c.Apps))
	}
}
