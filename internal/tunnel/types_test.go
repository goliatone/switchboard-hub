package tunnel

import "testing"

func TestCapabilitiesValidateRequiresForwarding(t *testing.T) {
	c := Capabilities{}
	if err := c.Validate(); err == nil {
		t.Fatal("expected forwarding validation error")
	}
}

func TestCapabilitiesValidateOAuthRequiresStableHost(t *testing.T) {
	c := Capabilities{
		OAuthSuitable:   true,
		HTTPSForwarding: true,
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected oauth stable hostname validation error")
	}
}

func TestCapabilitiesValidateOAuthUse(t *testing.T) {
	c := Capabilities{
		StableHostname:  true,
		HTTPSForwarding: true,
		OAuthSuitable:   true,
	}
	if err := c.ValidateOAuthUse("callback.dev.example.com"); err != nil {
		t.Fatalf("ValidateOAuthUse returned error: %v", err)
	}
}
