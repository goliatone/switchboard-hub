package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/sys"
)

var (
	statusCommandExists = sys.Exists
	statusRunCapture    = sys.RunCapture
	statusCheckCaddy    = defaultStatusCheckCaddy
)

type StatusReport struct {
	ConfigPath        string                   `json:"config_path"`
	TLD               string                   `json:"tld"`
	DNSIP             string                   `json:"dns_ip"`
	CaddyAdmin        string                   `json:"caddy_admin"`
	TLS               StatusTLSReport          `json:"tls"`
	Service           LaunchdServiceStatus     `json:"service"`
	ServiceError      string                   `json:"service_error,omitempty"`
	DNS               StatusCheckReport        `json:"dns"`
	Caddy             StatusCheckReport        `json:"caddy"`
	Apps              []StatusAppReport        `json:"apps"`
	TunnelHealth      []StatusTunnelHealthItem `json:"tunnel_health,omitempty"`
	TunnelHealthError string                   `json:"tunnel_health_error,omitempty"`
}

type StatusTLSReport struct {
	Enabled  bool     `json:"enabled"`
	Mode     string   `json:"mode"`
	Valid    bool     `json:"valid"`
	Error    string   `json:"error,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type StatusCheckReport struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	StartHint string `json:"start_hint,omitempty"`
}

type StatusAppReport struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
}

type StatusTunnelHealthItem struct {
	AppName        string `json:"app_name"`
	Provider       string `json:"provider,omitempty"`
	EndpointHost   string `json:"endpoint_host,omitempty"`
	Status         string `json:"status"`
	Message        string `json:"message,omitempty"`
	Error          string `json:"error,omitempty"`
	SessionSummary string `json:"session_summary,omitempty"`
}

func StatusReportInfo() (StatusReport, error) {
	p, err := cfgPath()
	if err != nil {
		return StatusReport{}, err
	}
	c, err := config.LoadOrDefault(p)
	if err != nil {
		return StatusReport{}, err
	}

	report := StatusReport{
		ConfigPath: p,
		TLD:        c.TLD,
		DNSIP:      c.DNS.IP,
		CaddyAdmin: c.Caddy.Admin,
		TLS: StatusTLSReport{
			Enabled:  c.Caddy.TLS.Enabled,
			Mode:     normalizeTLSMode(c.Caddy.TLS.Mode),
			Warnings: append([]string(nil), tlsWarnings(c)...),
		},
		Apps: make([]StatusAppReport, 0, len(c.Apps)),
	}

	if err := validateTLSConfig(c); err != nil {
		report.TLS.Valid = false
		report.TLS.Error = err.Error()
	} else {
		report.TLS.Valid = true
	}

	serviceStatus, serviceErr := ServiceStatusInfo()
	if serviceErr != nil {
		report.ServiceError = serviceErr.Error()
	} else {
		report.Service = serviceStatus
	}

	report.DNS = buildDNSStatus(c)
	report.Caddy = buildCaddyStatus(c, serviceStatus, serviceErr)

	apps := append([]config.App(nil), c.Apps...)
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	for _, a := range apps {
		report.Apps = append(report.Apps, StatusAppReport{
			Name: a.Name,
			Host: a.LocalHost,
			Port: a.LocalPort,
		})
	}

	health, hErr := appTunnelHealthStatusFromConfig(c)
	if hErr != nil {
		report.TunnelHealthError = hErr.Error()
		return report, nil
	}
	report.TunnelHealth = make([]StatusTunnelHealthItem, 0, len(health))
	for _, h := range health {
		item := StatusTunnelHealthItem{
			AppName:      h.AppName,
			Provider:     strings.TrimSpace(h.Provider),
			EndpointHost: h.EndpointHost,
		}
		switch {
		case item.Provider == "":
			item.Status = "none"
			item.Message = "no tunnel configured"
		case strings.TrimSpace(h.Err) != "":
			item.Status = "error"
			item.Error = strings.TrimSpace(h.Err)
		default:
			if h.Ready {
				item.Status = "ready"
			} else {
				item.Status = "not-ready"
			}
			item.Message = strings.TrimSpace(h.Message)
			item.SessionSummary = strings.TrimSpace(sessionSummary(h.SessionPID, h.StartedAt))
		}
		report.TunnelHealth = append(report.TunnelHealth, item)
	}

	return report, nil
}

func buildDNSStatus(c *config.Config) StatusCheckReport {
	if !statusCommandExists("dig") {
		return StatusCheckReport{
			Status:  "skipped",
			Message: "dig not found (skipping)",
		}
	}
	out, err := statusRunCapture("dig", "+short", "switchboard-hub-status."+c.TLD, "@"+c.DNS.IP)
	if err != nil {
		return StatusCheckReport{
			Status:  "error",
			Message: "dig failed: " + err.Error(),
		}
	}
	answer := strings.TrimSpace(out)
	if answer == "" {
		return StatusCheckReport{
			Status:  "warning",
			Message: "no answer (dnsmasq might not be running/configured)",
		}
	}
	return StatusCheckReport{
		Status:  "ok",
		Message: "ok (dig returned: " + answer + ")",
	}
}

func buildCaddyStatus(c *config.Config, serviceStatus LaunchdServiceStatus, serviceErr error) StatusCheckReport {
	if err := statusCheckCaddy(c.Caddy.Admin); err != nil {
		out := StatusCheckReport{
			Status:  "error",
			Message: "admin unreachable: " + err.Error(),
		}
		if serviceErr == nil && serviceStatus.Installed {
			out.StartHint = "sudo switchd service start"
		} else {
			out.StartHint = "sudo switchd caddy run"
		}
		return out
	}
	if serviceErr == nil && serviceStatus.Running {
		return StatusCheckReport{
			Status:  "ok",
			Message: "admin reachable (background service)",
		}
	}
	return StatusCheckReport{
		Status:  "ok",
		Message: "admin reachable (external/foreground)",
	}
}

func defaultStatusCheckCaddy(adminURL string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", strings.TrimRight(adminURL, "/")+"/config/", nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func Status() error {
	report, err := StatusReportInfo()
	if err != nil {
		return err
	}

	fmt.Println("Config:", report.ConfigPath)
	fmt.Println("TLD:   ", report.TLD)
	fmt.Println("DNS IP:", report.DNSIP)
	fmt.Println("Caddy:", report.CaddyAdmin)
	fmt.Printf("TLS:    enabled=%t mode=%s\n", report.TLS.Enabled, report.TLS.Mode)

	fmt.Println()
	fmt.Println("Checks:")

	if strings.TrimSpace(report.ServiceError) != "" {
		fmt.Println("- Service:", "error:", report.ServiceError)
	} else {
		st := report.Service
		switch {
		case st.Running:
			if st.Phase != "" {
				fmt.Printf("- Service: installed=%s running=%s ready=%s pid=%d phase=%s\n", yesNo(st.Installed), yesNo(true), yesNo(st.Ready), st.PID, st.Phase)
			} else {
				fmt.Printf("- Service: installed=%s running=%s ready=%s pid=%d\n", yesNo(st.Installed), yesNo(true), yesNo(st.Ready), st.PID)
			}
		case st.Stale:
			if st.StateError != "" {
				fmt.Printf("- Service: installed=%s running=%s stale_runtime_state pid=%d error=%s\n", yesNo(st.Installed), yesNo(false), st.PID, st.StateError)
			} else {
				fmt.Printf("- Service: installed=%s running=%s stale_runtime_state pid=%d\n", yesNo(st.Installed), yesNo(false), st.PID)
			}
		default:
			fmt.Printf("- Service: installed=%s running=%s ready=%s\n", yesNo(st.Installed), yesNo(false), yesNo(st.Ready))
		}
		if len(st.MissingEnvVars) > 0 {
			fmt.Printf("  Missing env for background resume: %s\n", strings.Join(st.MissingEnvVars, ", "))
			if strings.TrimSpace(st.EnvFilePath) != "" {
				fmt.Printf("  Add them to: %s\n", st.EnvFilePath)
			}
		}
	}

	if !report.TLS.Valid {
		fmt.Println("- TLS:", "invalid:", report.TLS.Error)
	} else {
		fmt.Println("- TLS:", "config ok")
	}
	for _, warning := range report.TLS.Warnings {
		fmt.Println("- TLS:", "warning:", warning)
	}

	fmt.Println("- DNS:", report.DNS.Message)
	fmt.Println("- Caddy:", report.Caddy.Message)
	if strings.TrimSpace(report.Caddy.StartHint) != "" {
		fmt.Println("  Start:", report.Caddy.StartHint)
	}

	fmt.Println("- Apps:", fmt.Sprintf("%d configured", len(report.Apps)))
	if len(report.Apps) == 0 {
		fmt.Println("  (none)")
		return nil
	}
	for _, item := range report.Apps {
		fmt.Printf("  - %s: host=%s port=%d\n", item.Name, item.Host, item.Port)
	}

	if strings.TrimSpace(report.TunnelHealthError) != "" {
		fmt.Println("- Tunnel health:", "error:", report.TunnelHealthError)
		return nil
	}
	fmt.Println("- Tunnel health:")
	for _, item := range report.TunnelHealth {
		if item.Provider == "" {
			fmt.Printf("  - %s: no tunnel configured\n", item.AppName)
			continue
		}
		base := fmt.Sprintf("  - %s: provider=%s host=%s", item.AppName, item.Provider, item.EndpointHost)
		if item.Error != "" {
			fmt.Printf("%s status=error %s\n", base, item.Error)
			continue
		}
		sessionInfo := strings.TrimSpace(item.SessionSummary)
		if sessionInfo != "" {
			sessionInfo = " " + sessionInfo
		}
		fmt.Printf("%s status=%s %s%s\n", base, item.Status, item.Message, sessionInfo)
	}

	return nil
}
