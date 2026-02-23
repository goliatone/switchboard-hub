package app

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/config"
)

func ValidateOAuthCallbackPath(callbackPath string) (string, error) {
	raw := strings.TrimSpace(callbackPath)
	if raw == "" {
		return "", fmt.Errorf("callback path is required")
	}
	if strings.Contains(raw, "://") {
		return "", fmt.Errorf("callback path must not include scheme or host")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid callback path %q: %w", callbackPath, err)
	}
	if u.IsAbs() || u.Host != "" {
		return "", fmt.Errorf("callback path must not include scheme or host")
	}
	if !strings.HasPrefix(u.Path, "/") {
		return "", fmt.Errorf("callback path must start with '/'")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("callback path must not include query string or fragment")
	}
	return u.Path, nil
}

func BuildGoogleRedirectURI(publicHost, callbackPath string) (string, error) {
	host, err := normalizePublicHost(publicHost)
	if err != nil {
		return "", err
	}
	path, err := ValidateOAuthCallbackPath(callbackPath)
	if err != nil {
		return "", err
	}
	return "https://" + host + path, nil
}

func EnsureEqualRedirectURI(authorizeURI, tokenURI string) error {
	a := strings.TrimSpace(authorizeURI)
	t := strings.TrimSpace(tokenURI)
	if a == "" || t == "" {
		return fmt.Errorf("both authorize and token redirect_uri values are required")
	}
	if a != t {
		return fmt.Errorf("redirect_uri mismatch: authorize=%q token=%q", a, t)
	}
	return nil
}

func OAuthGoogleEnable(appName, callbackPath string) error {
	p, c, err := loadConfigWithPath()
	if err != nil {
		return err
	}
	idx, _, err := appIndexByName(c, appName)
	if err != nil {
		return err
	}
	if _, err := configureGoogleOAuth(&c.Apps[idx], callbackPath); err != nil {
		return err
	}
	if err := config.Save(p, c); err != nil {
		return err
	}
	return nil
}

func OAuthGooglePrint(appName string) (string, error) {
	_, c, err := loadConfigWithPath()
	if err != nil {
		return "", err
	}
	idx, _, err := appIndexByName(c, appName)
	if err != nil {
		return "", err
	}
	a := c.Apps[idx]
	if !a.OAuth.Google.Enabled {
		return "", fmt.Errorf("google oauth is not enabled for app %q", a.Name)
	}
	expected, err := BuildGoogleRedirectURI(a.PublicEndpoint.Host, a.OAuth.Google.CallbackPath)
	if err != nil {
		return "", err
	}
	if err := EnsureEqualRedirectURI(expected, a.OAuth.Google.RedirectURI); err != nil {
		return "", err
	}

	block := fmt.Sprintf(`Google OAuth Redirect URI (copy exactly):
%s

Use this exact URI in:
- Google Cloud Console -> OAuth client -> Authorized redirect URIs
- authorize request redirect_uri
- token exchange redirect_uri`, a.OAuth.Google.RedirectURI)
	return block, nil
}

func configureGoogleOAuth(a *config.App, callbackPath string) (string, error) {
	if a == nil {
		return "", fmt.Errorf("app config is nil")
	}
	if strings.TrimSpace(a.PublicEndpoint.Host) == "" {
		return "", fmt.Errorf("app %q has no public endpoint host configured (run `switchd app expose` first)", a.Name)
	}
	redirectURI, err := BuildGoogleRedirectURI(a.PublicEndpoint.Host, callbackPath)
	if err != nil {
		return "", err
	}
	path, err := ValidateOAuthCallbackPath(callbackPath)
	if err != nil {
		return "", err
	}
	a.OAuth.Google.Enabled = true
	a.OAuth.Google.CallbackPath = path
	a.OAuth.Google.RedirectURI = redirectURI
	return redirectURI, nil
}
