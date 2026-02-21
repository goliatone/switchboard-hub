package caddy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
)

func BuildJSON(c *config.Config) ([]byte, error) {
	routes := buildRoutes(c)
	cfg := map[string]any{
		"admin": map[string]any{
			"listen": adminListenAddress(c.Caddy.Admin),
		},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"switchboard_hub_http": map[string]any{
						"listen": c.Caddy.Listen,
						"routes": routes,
					},
				},
			},
		},
	}

	if c.Caddy.TLS.Enabled {
		servers := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)
		servers["switchboard_hub_https"] = map[string]any{
			"listen": c.Caddy.TLS.Listen,
			"routes": routes,
			"tls_connection_policies": []any{
				map[string]any{},
			},
		}

		mode := strings.ToLower(strings.TrimSpace(c.Caddy.TLS.Mode))
		if mode == "" {
			mode = "internal"
		}

		switch mode {
		case "internal":
			subjects := uniqueHosts(c.Routes)
			if len(subjects) > 0 {
				cfg["apps"].(map[string]any)["tls"] = map[string]any{
					"automation": map[string]any{
						"policies": []any{
							map[string]any{
								"subjects": subjects,
								"issuers": []any{
									map[string]any{
										"module": "internal",
									},
								},
							},
						},
					},
				}
			}
		case "file":
			certFile := strings.TrimSpace(c.Caddy.TLS.CertFile)
			keyFile := strings.TrimSpace(c.Caddy.TLS.KeyFile)
			if certFile == "" || keyFile == "" {
				return nil, fmt.Errorf("tls mode file requires caddy.tls.cert_file and caddy.tls.key_file")
			}
			cfg["apps"].(map[string]any)["tls"] = map[string]any{
				"certificates": map[string]any{
					"load_files": []any{
						map[string]any{
							"certificate": certFile,
							"key":         keyFile,
						},
					},
				},
			}
		default:
			return nil, fmt.Errorf("unsupported caddy tls mode %q (expected internal or file)", mode)
		}
	}

	return json.MarshalIndent(cfg, "", "  ")
}

func buildRoutes(c *config.Config) []any {
	routes := make([]any, 0, len(c.Routes)+1)
	for _, r := range c.Routes {
		routes = append(routes, map[string]any{
			"match": []any{
				map[string]any{
					"host": []string{r.Host},
				},
			},
			"handle": []any{
				map[string]any{
					"handler": "reverse_proxy",
					"upstreams": []any{
						map[string]any{"dial": r.Dial},
					},
				},
			},
			"terminal": true,
		})
	}

	routes = append(routes, map[string]any{
		"handle": []any{
			map[string]any{
				"handler":     "static_response",
				"status_code": 404,
				"body":        "switchboard-hub: unknown host\n",
			},
		},
		"terminal": true,
	})

	return routes
}

func uniqueHosts(routes []config.Route) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		host := strings.ToLower(strings.TrimSpace(r.Host))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	sort.Strings(out)
	return out
}

func adminListenAddress(admin string) string {
	raw := strings.TrimSpace(admin)
	if raw == "" {
		return "127.0.0.1:2019"
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "127.0.0.1:2019"
	}
	return u.Host
}

func LoadConfig(adminBase string, cfg []byte) error {
	base := strings.TrimRight(adminBase, "/")
	url := base + "/load"

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("POST", url, bytes.NewReader(cfg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("caddy /load failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func WriteBootstrapCaddyfile(path string) error {
	caddyfile := `{
  admin 127.0.0.1:2019
}

:80 {
  respond "switchboard-hub bootstrap (run switchd apply)\n" 200
}
`
	return os.WriteFile(path, []byte(caddyfile), 0o644)
}
