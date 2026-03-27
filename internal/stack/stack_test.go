package stack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedAppName(t *testing.T) {
	if got, want := GeneratedAppName("carina", "app"), "carina-app"; got != want {
		t.Fatalf("GeneratedAppName()=%q want=%q", got, want)
	}
}

func TestParentDomain(t *testing.T) {
	got, err := parentDomain("app.carina.getctx.com")
	if err != nil {
		t.Fatalf("parentDomain returned error: %v", err)
	}
	if got != "carina.getctx.com" {
		t.Fatalf("parentDomain()=%q want=%q", got, "carina.getctx.com")
	}

	got, err = parentDomain("carina.getctx.com")
	if err != nil {
		t.Fatalf("parentDomain returned error: %v", err)
	}
	if got != "getctx.com" {
		t.Fatalf("parentDomain()=%q want=%q", got, "getctx.com")
	}

	if _, err := parentDomain("localhost"); err == nil {
		t.Fatal("expected parentDomain to fail for a single-label host")
	}
}

func TestLoadValidateAndResolveDefaults(t *testing.T) {
	st := mustLoadStack(t, []byte(`
version: 1
name: carina

defaults:
  provider: cloudflare
  expose: true
  up: true

services:
  - name: app
    local_port: 8383
    public_host: app.carina.getctx.com

  - name: simulator
    local_port: 8090
    public_host: carina.getctx.com
`))

	resolved, err := st.Resolve("getctx.com")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	appSvc := resolved.mustService(t, "app")
	if appSvc.GeneratedAppName != "carina-app" {
		t.Fatalf("unexpected generated app name: %q", appSvc.GeneratedAppName)
	}
	if appSvc.LocalHost != "carina-app.getctx.com" {
		t.Fatalf("unexpected default local host: %q", appSvc.LocalHost)
	}
	if appSvc.Provider != "cloudflare" || !appSvc.Expose || !appSvc.Up {
		t.Fatalf("unexpected defaults applied to app service: %#v", appSvc)
	}

	simSvc := resolved.mustService(t, "simulator")
	if simSvc.LocalHost != "carina-simulator.getctx.com" {
		t.Fatalf("unexpected simulator local host: %q", simSvc.LocalHost)
	}
	if simSvc.PublicHost != "carina.getctx.com" {
		t.Fatalf("unexpected simulator public host: %q", simSvc.PublicHost)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(path, []byte(`
version: 1
name: carina
services:
  - name: app
    local_port: 8383
`), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	st, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile returned error: %v", err)
	}
	if st.Name != "carina" {
		t.Fatalf("unexpected stack name: %q", st.Name)
	}
}

func TestRenderEnvLines(t *testing.T) {
	st := mustLoadStack(t, []byte(`
version: 1
name: carina

services:
  - name: app
    local_port: 8383
    public_host: app.carina.getctx.com
  - name: simulator
    local_port: 8090
    public_host: carina.getctx.com

outputs:
  APP_HTTP__BASE_URL: "https://{{ service \"app\" \"public_host\" }}"
  APP_HTTP__LOCAL_PORT: "{{ service \"app\" \"local_port\" }}"
  APP_HTTP__APP_NAME: "{{ service \"app\" \"generated_app_name\" }}"
  APP_ADMIN_AUTH__COOKIE_DOMAIN: "{{ parent_domain (service \"simulator\" \"public_host\") }}"
`))

	resolved, err := st.Resolve("getctx.com")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	lines, err := resolved.RenderEnvLines()
	if err != nil {
		t.Fatalf("RenderEnvLines returned error: %v", err)
	}

	want := []string{
		"APP_ADMIN_AUTH__COOKIE_DOMAIN=getctx.com",
		"APP_HTTP__APP_NAME=carina-app",
		"APP_HTTP__BASE_URL=https://app.carina.getctx.com",
		"APP_HTTP__LOCAL_PORT=8383",
	}
	if len(lines) != len(want) {
		t.Fatalf("unexpected line count %d want %d: %#v", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d=%q want %q", i, lines[i], want[i])
		}
	}
}

func TestRenderErrors(t *testing.T) {
	st := mustLoadStack(t, []byte(`
version: 1
name: carina

services:
  - name: app
    local_port: 8383

outputs:
  BROKEN: "{{ service \"missing\" \"public_host\" }}"
`))

	resolved, err := st.Resolve("getctx.com")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if _, err := resolved.RenderOutputs(); err == nil || !strings.Contains(err.Error(), `service "missing" not found`) {
		t.Fatalf("expected missing service error, got %v", err)
	}

	st = mustLoadStack(t, []byte(`
version: 1
name: carina

services:
  - name: app
    local_port: 8383

outputs:
  BROKEN: "{{ service \"app\" \"unknown\" }}"
`))
	resolved, err = st.Resolve("getctx.com")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if _, err := resolved.RenderOutputs(); err == nil || !strings.Contains(err.Error(), "unsupported service field") {
		t.Fatalf("expected unsupported field error, got %v", err)
	}
}

func TestValidationFailures(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "unsupported version",
			yaml: `
version: 2
name: carina
services:
  - name: app
    local_port: 8383
`,
			want: "unsupported stack version",
		},
		{
			name: "missing services",
			yaml: `
version: 1
name: carina
services: []
`,
			want: "services must contain at least one service",
		},
		{
			name: "duplicate services",
			yaml: `
version: 1
name: carina
services:
  - name: app
    local_port: 8383
  - name: app
    local_port: 8090
`,
			want: "duplicate service name",
		},
		{
			name: "bad port",
			yaml: `
version: 1
name: carina
services:
  - name: app
    local_port: 0
`,
			want: "invalid local_port",
		},
		{
			name: "bad local host",
			yaml: `
version: 1
name: carina
services:
  - name: app
    local_port: 8383
    local_host: http://bad host
`,
			want: "local_host",
		},
		{
			name: "bad public host",
			yaml: `
version: 1
name: carina
services:
  - name: app
    local_port: 8383
    public_host: bad host
`,
			want: "public_host",
		},
		{
			name: "colliding generated app names",
			yaml: `
version: 1
name: carina
services:
  - name: app
    local_port: 8383
  - name: app!
    local_port: 8090
`,
			want: "collision",
		},
		{
			name: "duplicate public host",
			yaml: `
version: 1
name: carina
services:
  - name: app
    local_port: 8383
    public_host: app.carina.getctx.com
  - name: simulator
    local_port: 8090
    public_host: app.carina.getctx.com
`,
			want: "duplicate public_host",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadBytes([]byte(tc.yaml))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestResolveRequiresTLDForDerivedLocalHost(t *testing.T) {
	st := mustLoadStack(t, []byte(`
version: 1
name: carina
services:
  - name: app
    local_port: 8383
`))

	if _, err := st.Resolve(""); err == nil || !strings.Contains(err.Error(), "tld is required") {
		t.Fatalf("expected tld requirement error, got %v", err)
	}
}

func TestResolveRejectsDerivedLocalHostCollision(t *testing.T) {
	st := mustLoadStack(t, []byte(`
version: 1
name: carina
services:
  - name: app
    local_port: 8383
  - name: simulator
    local_port: 8090
    local_host: carina-app.test
`))

	if _, err := st.Resolve("test"); err == nil || !strings.Contains(err.Error(), "duplicate local_host") {
		t.Fatalf("expected duplicate local_host error, got %v", err)
	}
}

func mustLoadStack(t *testing.T, yaml []byte) *Stack {
	t.Helper()
	st, err := LoadBytes(yaml)
	if err != nil {
		t.Fatalf("LoadBytes returned error: %v", err)
	}
	return st
}

func (r *ResolvedStack) mustService(t *testing.T, name string) ResolvedService {
	t.Helper()
	svc, ok := r.lookupService(name)
	if !ok {
		t.Fatalf("service %q not found", name)
	}
	return svc
}
