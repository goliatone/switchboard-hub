package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/goliatone/switchboard-hub/pkg/switchboard"
)

type appListViewModel struct {
	Rows        []appListRow
	HealthError string
}

type appListRow struct {
	Name           string
	LocalHost      string
	Port           int
	PublicHost     string
	OAuth          string
	TunnelLabel    string
	TunnelState    string
	TunnelHealth   string
	Provider       string
	EndpointHost   string
	SessionID      string
	SessionPID     int
	SessionStarted string
	TunnelMessage  string
	TunnelError    string
}

func buildAppListViewModel(apps []switchboard.App, health []switchboard.AppTunnelHealth, healthErr error) appListViewModel {
	model := appListViewModel{}
	if healthErr != nil {
		model.HealthError = strings.TrimSpace(healthErr.Error())
	}
	healthByApp := map[string]switchboard.AppTunnelHealth{}
	healthKnown := healthErr == nil
	if healthKnown {
		for _, item := range health {
			healthByApp[strings.ToLower(strings.TrimSpace(item.AppName))] = item
		}
	}

	rows := make([]appListRow, 0, len(apps))
	for _, a := range apps {
		key := strings.ToLower(strings.TrimSpace(a.Name))
		rows = append(rows, buildAppListRow(a, healthByApp[key], healthKnown))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	model.Rows = rows
	return model
}

func buildAppListRow(a switchboard.App, health switchboard.AppTunnelHealth, healthKnown bool) appListRow {
	oauth := "off"
	if a.OAuth.Google.Enabled {
		oauth = "google"
	}

	row := appListRow{
		Name:           a.Name,
		LocalHost:      a.LocalHost,
		Port:           a.LocalPort,
		PublicHost:     valueOrDash(a.PublicEndpoint.Host),
		OAuth:          oauth,
		TunnelLabel:    switchboardAppSessionLabel(a, health, healthKnown),
		TunnelState:    switchboardAppSessionState(a),
		TunnelHealth:   appTunnelHealthState(a, health, healthKnown),
		Provider:       strings.TrimSpace(health.Provider),
		EndpointHost:   strings.TrimSpace(health.EndpointHost),
		SessionID:      strings.TrimSpace(health.SessionID),
		SessionPID:     health.SessionPID,
		SessionStarted: strings.TrimSpace(health.StartedAt),
		TunnelMessage:  strings.TrimSpace(health.Message),
		TunnelError:    strings.TrimSpace(health.Err),
	}

	if row.Provider == "" {
		row.Provider = strings.TrimSpace(a.PublicEndpoint.Provider)
	}
	if row.EndpointHost == "" {
		row.EndpointHost = strings.TrimSpace(a.PublicEndpoint.Host)
	}
	if row.SessionID == "" {
		row.SessionID = strings.TrimSpace(a.PublicEndpoint.ActiveSessionID)
	}
	if row.SessionPID == 0 {
		row.SessionPID = a.PublicEndpoint.ActiveSessionPID
	}
	if row.SessionStarted == "" {
		row.SessionStarted = strings.TrimSpace(a.PublicEndpoint.ActiveSessionStarted)
	}

	return row
}

func buildAppListTableRows(model appListViewModel) [][]string {
	rows := make([][]string, 0, len(model.Rows))
	for _, row := range model.Rows {
		rows = append(rows, []string{
			row.Name,
			row.LocalHost,
			fmt.Sprintf("%d", row.Port),
			row.PublicHost,
			row.OAuth,
			row.TunnelLabel,
		})
	}
	return rows
}

func appListFilterValue(row appListRow) string {
	parts := []string{
		row.Name,
		row.LocalHost,
		row.PublicHost,
		row.Provider,
		row.EndpointHost,
		row.TunnelLabel,
	}
	return strings.ToLower(strings.Join(parts, " "))
}

func switchboardAppSessionState(a switchboard.App) string {
	if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) != "" {
		return "active"
	}
	if strings.TrimSpace(a.PublicEndpoint.Host) != "" {
		return "idle"
	}
	return "none"
}

func appTunnelHealthState(a switchboard.App, health switchboard.AppTunnelHealth, healthKnown bool) string {
	state := switchboardAppSessionState(a)
	if state == "none" {
		return "none"
	}
	if !healthKnown || strings.TrimSpace(health.Err) != "" {
		return "unknown"
	}
	if health.Ready {
		return "ok"
	}
	return "warning"
}
