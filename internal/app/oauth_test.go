package app

import (
	"strings"
	"testing"

	"github.com/goliatone/switchboard-hub/internal/config"
)

func TestValidateOAuthCallbackPath(t *testing.T) {
	got, err := ValidateOAuthCallbackPath("/admin/esign/integrations/google/callback")
	if err != nil {
		t.Fatalf("ValidateOAuthCallbackPath returned error: %v", err)
	}
	if got != "/admin/esign/integrations/google/callback" {
		t.Fatalf("unexpected callback path: %q", got)
	}
}

func TestValidateOAuthCallbackPathRejectsAbsoluteURL(t *testing.T) {
	if _, err := ValidateOAuthCallbackPath("https://example.com/callback"); err == nil {
		t.Fatal("expected absolute URL callback path error")
	}
}

func TestBuildGoogleRedirectURI(t *testing.T) {
	got, err := BuildGoogleRedirectURI("esign-oauth.dev.example.com", "/cb")
	if err != nil {
		t.Fatalf("BuildGoogleRedirectURI returned error: %v", err)
	}
	if got != "https://esign-oauth.dev.example.com/cb" {
		t.Fatalf("unexpected redirect URI: %q", got)
	}
}

func TestEnsureEqualRedirectURI(t *testing.T) {
	match := "https://esign-oauth.dev.example.com/cb"
	if err := EnsureEqualRedirectURI(match, match); err != nil {
		t.Fatalf("EnsureEqualRedirectURI returned error: %v", err)
	}
	if err := EnsureEqualRedirectURI(match, match+"/"); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestConfigureGoogleOAuthPersistsUnderAppState(t *testing.T) {
	a := &config.App{
		Name:      "esign",
		LocalHost: "esign.test",
		LocalPort: 3000,
		PublicEndpoint: config.AppPublicEndpoint{
			Provider: "cloudflare",
			Host:     "esign-oauth.dev.example.com",
		},
	}
	redirect, err := configureGoogleOAuth(a, "/admin/esign/integrations/google/callback")
	if err != nil {
		t.Fatalf("configureGoogleOAuth returned error: %v", err)
	}
	if !a.OAuth.Google.Enabled {
		t.Fatal("expected google oauth enabled")
	}
	if !strings.Contains(redirect, "esign-oauth.dev.example.com") {
		t.Fatalf("unexpected redirect URI: %q", redirect)
	}
	if a.OAuth.Google.RedirectURI != redirect {
		t.Fatalf("state redirect_uri not persisted: got %q want %q", a.OAuth.Google.RedirectURI, redirect)
	}
}
