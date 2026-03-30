package main

import (
	"fmt"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
)

type textField struct {
	Key   string
	Value string
}

func (o cliOutput) printSection(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}
	fmt.Println(o.styles().section.Render(title))
}

func (o cliOutput) printSpacer() {
	fmt.Println()
}

func (o cliOutput) printFields(fields []textField) {
	filtered := make([]textField, 0, len(fields))
	width := 0
	for _, field := range fields {
		field.Key = strings.TrimSpace(field.Key)
		field.Value = strings.TrimSpace(field.Value)
		if field.Key == "" || field.Value == "" {
			continue
		}
		filtered = append(filtered, field)
		if len(field.Key) > width {
			width = len(field.Key)
		}
	}
	if len(filtered) == 0 {
		return
	}
	styles := o.styles()
	for _, field := range filtered {
		label := padCell(field.Key+":", width+1)
		fmt.Printf("%s %s\n", styles.key.Render(label), field.Value)
	}
}

func (o cliOutput) printBullets(items []string) {
	if len(items) == 0 {
		fmt.Println(o.styles().empty.Render("(none)"))
		return
	}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		fmt.Printf("  - %s\n", item)
	}
}

func (o cliOutput) printMutedLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	fmt.Println(o.styles().muted.Render(line))
}

func (o cliOutput) printStatusLine(label, status, summary string) {
	fmt.Println(renderStatusCheckLine(label, status, summary, o.styles()))
}

func buildRouteTableRows(routes []config.Route) [][]string {
	rows := make([][]string, 0, len(routes))
	for _, route := range routes {
		rows = append(rows, []string{route.Host, route.Dial})
	}
	return rows
}

func renderRoutesPlain(_ cliOutput, routes []config.Route) error {
	if len(routes) == 0 {
		fmt.Println("(no routes)")
		return nil
	}
	for _, route := range routes {
		fmt.Printf("%-35s -> %s\n", route.Host, route.Dial)
	}
	return nil
}

func renderAppListPlain(out cliOutput, model appListViewModel) error {
	if len(model.Rows) == 0 {
		fmt.Println("(no apps configured)")
		renderAppListHealthWarnings(out, model)
		return nil
	}
	out.printTable([]string{"NAME", "LOCAL_HOST", "PORT", "PUBLIC_HOST", "OAUTH", "TUNNEL"}, buildAppListTableRows(model))
	renderAppListHealthWarnings(out, model)
	return nil
}

func renderAppListHealthWarnings(out cliOutput, model appListViewModel) {
	seen := map[string]struct{}{}
	if detail := strings.TrimSpace(model.HealthError); detail != "" {
		seen[detail] = struct{}{}
		out.warn("Tunnel health unavailable", map[string]any{"detail": detail})
	}
	for _, row := range model.Rows {
		detail := strings.TrimSpace(row.TunnelError)
		if detail == "" {
			continue
		}
		if _, ok := seen[detail]; ok {
			continue
		}
		seen[detail] = struct{}{}
		out.warn("Tunnel health unavailable", map[string]any{"detail": detail})
	}
}

func buildStatusAppTableRows(apps []app.StatusAppReport) [][]string {
	rows := make([][]string, 0, len(apps))
	for _, item := range apps {
		rows = append(rows, []string{item.Name, item.Host, fmt.Sprintf("%d", item.Port)})
	}
	return rows
}

func buildStatusTunnelTableRows(items []app.StatusTunnelHealthItem) [][]string {
	rows := make([][]string, 0, len(items))
	for _, item := range items {
		detail := strings.TrimSpace(item.Message)
		if strings.TrimSpace(item.Error) != "" {
			detail = strings.TrimSpace(item.Error)
		}
		if session := strings.TrimSpace(item.SessionSummary); session != "" {
			if detail == "" {
				detail = session
			} else {
				detail += " " + session
			}
		}
		rows = append(rows, []string{
			item.AppName,
			valueOrDash(item.Provider),
			valueOrDash(item.EndpointHost),
			valueOrDash(item.Status),
			valueOrDash(detail),
		})
	}
	return rows
}

func (r *runContext) renderStackReportPlain(model stackReportViewModel) error {
	r.out.printSection("Stack")
	r.out.printFields([]textField{
		{Key: "command", Value: valueOrDash(model.Command)},
		{Key: "name", Value: valueOrDash(model.StackName)},
		{Key: "file", Value: valueOrDash(model.StackFile)},
		{Key: "changes", Value: boolLabel(model.HasChanges)},
		{Key: "unsafe", Value: boolLabel(model.HasUnsafe)},
	})
	r.out.printSpacer()
	if len(model.Rows) == 0 {
		r.out.printMutedLine("(no services)")
	} else {
		r.out.printTable([]string{"SERVICE", "APP", "LOCAL_HOST", "PORT", "PUBLIC_HOST", "PROVIDER", "SESSION", "DRIFT", "ACTIONS"}, buildStackReportTableRows(model))
	}
	if len(model.Collisions) > 0 {
		r.out.printSpacer()
		r.out.printSection("Collisions")
		r.out.printBullets(model.Collisions)
	}
	if len(model.Orphans) > 0 {
		r.out.printSpacer()
		r.out.printSection("Orphans")
		r.out.printBullets(model.Orphans)
	}
	return nil
}

func renderServiceStatusPlain(out cliOutput, st app.LaunchdServiceStatus) error {
	out.printSection("Service")
	out.printStatusLine("health", serviceStatusHealth(st), serviceStatusSummary(st))
	out.printFields([]textField{
		{Key: "label", Value: st.Label},
		{Key: "phase", Value: st.Phase},
		{Key: "pid", Value: intValueOrBlank(st.PID)},
		{Key: "caddy pid", Value: intValueOrBlank(st.CaddyPID)},
		{Key: "started at", Value: st.StartedAt},
	})
	out.printSpacer()
	out.printSection("Environment")
	out.printFields([]textField{
		{Key: "config", Value: st.ConfigPath},
		{Key: "env file", Value: st.EnvFilePath},
		{Key: "required", Value: joinOrNone(st.RequiredEnvVars)},
		{Key: "configured", Value: joinOrNone(st.ConfiguredEnvVars)},
		{Key: "missing", Value: joinOrNone(st.MissingEnvVars)},
	})
	out.printSpacer()
	out.printSection("Paths")
	out.printFields([]textField{
		{Key: "plist", Value: st.PlistPath},
		{Key: "state", Value: st.RuntimeStatePath},
		{Key: "logs", Value: st.LogDir},
		{Key: "state err", Value: st.StateError},
	})
	next := serviceStatusNextSteps(st)
	if len(next) > 0 {
		out.printSpacer()
		out.printSection("Next")
		out.printBullets(next)
	}
	return nil
}

func renderStatusReport(out cliOutput, report app.StatusReport) {
	out.printSection("Configuration")
	out.printFields([]textField{
		{Key: "config", Value: report.ConfigPath},
		{Key: "tld", Value: report.TLD},
		{Key: "dns", Value: report.DNSIP},
		{Key: "caddy", Value: report.CaddyAdmin},
		{Key: "tls", Value: fmt.Sprintf("enabled=%t mode=%s", report.TLS.Enabled, report.TLS.Mode)},
	})
	out.printSpacer()
	out.printSection("Checks")
	if strings.TrimSpace(report.ServiceError) != "" {
		out.printStatusLine("service", "error", report.ServiceError)
	} else {
		out.printStatusLine("service", serviceSummaryStatus(report), serviceSummary(report))
		if len(report.Service.MissingEnvVars) > 0 {
			line := "missing env for background resume: " + strings.Join(report.Service.MissingEnvVars, ", ")
			if strings.TrimSpace(report.Service.EnvFilePath) != "" {
				line += " (" + report.Service.EnvFilePath + ")"
			}
			out.printMutedLine(line)
		}
	}
	out.printStatusLine("tls", tlsSummaryStatus(report.TLS), tlsSummary(report.TLS))
	for _, warning := range report.TLS.Warnings {
		out.printMutedLine("tls warning: " + warning)
	}
	out.printStatusLine("dns", report.DNS.Status, summaryOrDefault(report.DNS.Message, report.DNS.Status))
	out.printStatusLine("caddy", report.Caddy.Status, summaryOrDefault(report.Caddy.Message, report.Caddy.Status))
	if strings.TrimSpace(report.Caddy.StartHint) != "" {
		out.printMutedLine("start: " + report.Caddy.StartHint)
	}
	out.printSpacer()
	out.printSection("Apps")
	if len(report.Apps) == 0 {
		out.printMutedLine("(none)")
	} else {
		out.printTable([]string{"NAME", "HOST", "PORT"}, buildStatusAppTableRows(report.Apps))
	}
	out.printSpacer()
	out.printSection("Tunnel Health")
	if strings.TrimSpace(report.TunnelHealthError) != "" {
		out.printStatusLine("tunnels", "error", report.TunnelHealthError)
		return
	}
	if len(report.TunnelHealth) == 0 {
		out.printMutedLine("(none)")
		return
	}
	out.printTable([]string{"APP", "PROVIDER", "HOST", "STATUS", "DETAIL"}, buildStatusTunnelTableRows(report.TunnelHealth))
}

func serviceStatusNextSteps(st app.LaunchdServiceStatus) []string {
	steps := make([]string, 0, 3)
	if len(st.MissingEnvVars) > 0 && strings.TrimSpace(st.EnvFilePath) != "" {
		steps = append(steps, "Add missing values to "+st.EnvFilePath)
	}
	if !st.Running && st.Installed {
		steps = append(steps, "Run sudo switchd service start")
	}
	if !st.Installed {
		steps = append(steps, "Run sudo switchd service install")
	}
	if strings.TrimSpace(st.StateError) != "" {
		steps = append(steps, "Inspect launchd state and service logs")
	}
	return steps
}

func intValueOrBlank(v int) string {
	if v <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", v)
}
