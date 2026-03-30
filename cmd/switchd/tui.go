package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/goliatone/switchboard-hub/internal/app"
)

type serviceLogLineMsg struct {
	gen   int
	event app.ServiceLogEvent
}

type serviceLogErrMsg struct {
	gen int
	err error
}

type serviceLogDoneMsg struct {
	gen int
}

type statusLoadedMsg struct {
	report app.StatusReport
	err    error
}

type statusTickMsg time.Time

type serviceLogTUIModel struct {
	styles       cliStyles
	viewport     viewport.Model
	width        int
	height       int
	ready        bool
	lines        []string
	lineLimit    int
	initialLines int
	stream       string
	follow       bool
	paused       bool
	gen          int
	cancel       context.CancelFunc
	msgs         chan tea.Msg
	lastErr      string
	command      string
}

type statusTUIModel struct {
	styles      cliStyles
	viewport    viewport.Model
	width       int
	height      int
	ready       bool
	report      app.StatusReport
	lastErr     string
	lastUpdated time.Time
}

func runServiceLogTUI(opts app.ServiceLogOptions, commandName string, styles cliStyles) error {
	model := &serviceLogTUIModel{
		styles:       styles,
		stream:       opts.Stream,
		follow:       opts.Follow,
		lineLimit:    1500,
		initialLines: opts.Lines,
		command:      commandName,
	}
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func runStatusTUI(load func() (app.StatusReport, error), styles cliStyles) error {
	model := &statusTUIModel{
		styles: styles,
	}
	p := tea.NewProgram(model, tea.WithAltScreen())
	statusLoader = load
	defer func() { statusLoader = statusReportInfo }()
	_, err := p.Run()
	return err
}

func (m *serviceLogTUIModel) Init() tea.Cmd {
	return m.restartStream()
}

func (m *serviceLogTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.viewport = viewport.New(msg.Width, max(5, msg.Height-5))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = max(5, msg.Height-5)
		}
		m.viewport.SetContent(strings.Join(m.lines, "\n"))
		if !m.paused {
			m.viewport.GotoBottom()
		}
		return m, nil
	case serviceLogLineMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.lines = append(m.lines, m.renderServiceLogLine(msg.event))
		if len(m.lines) > m.lineLimit {
			m.lines = m.lines[len(m.lines)-m.lineLimit:]
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			if !m.paused {
				m.viewport.GotoBottom()
			}
		}
		return m, waitForTUIMessage(m.msgs)
	case serviceLogErrMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		m.lastErr = msg.err.Error()
		return m, waitForTUIMessage(m.msgs)
	case serviceLogDoneMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.stop()
			return m, tea.Quit
		case "p":
			m.paused = !m.paused
			if !m.paused && m.ready {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "c":
			m.lines = nil
			if m.ready {
				m.viewport.SetContent("")
			}
			return m, nil
		case "s":
			m.stream = nextStreamMode(m.stream)
			m.lastErr = ""
			m.lines = nil
			if m.ready {
				m.viewport.SetContent("")
			}
			return m, m.restartStream()
		case "f":
			m.follow = !m.follow
			m.lastErr = ""
			return m, m.restartStream()
		}
		if m.ready {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *serviceLogTUIModel) View() string {
	title := m.styles.title.Render("switchd service log")
	statusBits := []string{
		"stream=" + m.stream,
		"follow=" + boolLabel(m.follow),
		"paused=" + boolLabel(m.paused),
	}
	header := lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", m.styles.muted.Render(strings.Join(statusBits, "  ")))
	footer := m.styles.muted.Render("keys: q quit  p pause  f toggle-follow  s cycle-stream  c clear")
	body := m.styles.muted.Render("Waiting for service logs...")
	if m.ready {
		body = m.viewport.View()
	}
	if m.lastErr != "" {
		footer = m.styles.statusErr.Render(m.lastErr) + "\n" + footer
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *serviceLogTUIModel) stop() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *serviceLogTUIModel) restartStream() tea.Cmd {
	m.stop()
	m.gen++
	gen := m.gen
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.msgs = make(chan tea.Msg, 256)
	msgs := m.msgs
	go func() {
		err := app.ServiceLogWithContext(ctx, app.ServiceLogOptions{
			Lines:  m.initialLines,
			Follow: m.follow,
			Stream: m.stream,
			Stdout: io.Discard,
			Stderr: io.Discard,
			EventSink: func(event app.ServiceLogEvent) {
				msgs <- serviceLogLineMsg{gen: gen, event: event}
			},
		})
		if err != nil {
			msgs <- serviceLogErrMsg{gen: gen, err: err}
			return
		}
		msgs <- serviceLogDoneMsg{gen: gen}
	}()
	return waitForTUIMessage(m.msgs)
}

func (m *serviceLogTUIModel) renderServiceLogLine(event app.ServiceLogEvent) string {
	prefix := m.styles.statusDim.Render("stdout")
	if event.Stream == "stderr" {
		prefix = m.styles.statusErr.Render("stderr")
	}
	if strings.TrimSpace(event.Stream) == "" {
		return event.Line
	}
	return prefix + " " + event.Line
}

var statusLoader = statusReportInfo

func (m *statusTUIModel) Init() tea.Cmd {
	return tea.Batch(loadStatusCmd(), statusTickCmd())
}

func (m *statusTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.viewport = viewport.New(msg.Width, max(5, msg.Height-4))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = max(5, msg.Height-4)
		}
		m.viewport.SetContent(renderStatusReportTUI(m.report, m.styles))
		return m, nil
	case statusLoadedMsg:
		if msg.err != nil {
			m.lastErr = msg.err.Error()
		} else {
			m.report = msg.report
			m.lastErr = ""
			m.lastUpdated = time.Now()
		}
		if m.ready {
			m.viewport.SetContent(renderStatusReportTUI(m.report, m.styles))
			m.viewport.GotoTop()
		}
		return m, nil
	case statusTickMsg:
		return m, tea.Batch(loadStatusCmd(), statusTickCmd())
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "r":
			return m, loadStatusCmd()
		}
		if m.ready {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *statusTUIModel) View() string {
	title := m.styles.title.Render("switchd status")
	meta := []string{"r refresh", "q quit"}
	if !m.lastUpdated.IsZero() {
		meta = append(meta, "updated="+m.lastUpdated.Format("15:04:05"))
	}
	header := lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", m.styles.muted.Render(strings.Join(meta, "  ")))
	body := m.styles.muted.Render("Loading status...")
	if m.ready {
		body = m.viewport.View()
	}
	if m.lastErr != "" {
		body = m.styles.statusErr.Render(m.lastErr) + "\n\n" + body
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

func waitForTUIMessage(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func loadStatusCmd() tea.Cmd {
	return func() tea.Msg {
		report, err := statusLoader()
		return statusLoadedMsg{report: report, err: err}
	}
}

func statusTickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg {
		return statusTickMsg(t)
	})
}

func renderStatusReportTUI(report app.StatusReport, styles cliStyles) string {
	lines := []string{
		styles.section.Render("Configuration"),
		fmt.Sprintf("%s %s", styles.key.Render("config"), report.ConfigPath),
		fmt.Sprintf("%s %s", styles.key.Render("tld"), report.TLD),
		fmt.Sprintf("%s %s", styles.key.Render("dns"), report.DNSIP),
		fmt.Sprintf("%s %s", styles.key.Render("caddy"), report.CaddyAdmin),
		"",
		styles.section.Render("Checks"),
		renderStatusCheckLine("service", serviceSummary(report), styles),
		renderStatusCheckLine("tls", tlsSummary(report.TLS), styles),
		renderStatusCheckLine("dns", report.DNS.Message, styles),
		renderStatusCheckLine("caddy", report.Caddy.Message, styles),
		"",
		styles.section.Render("Apps"),
	}
	if len(report.Apps) == 0 {
		lines = append(lines, styles.muted.Render("(none)"))
	} else {
		for _, item := range report.Apps {
			lines = append(lines, fmt.Sprintf("%s %s:%d", styles.key.Render(item.Name), item.Host, item.Port))
		}
	}
	lines = append(lines, "", styles.section.Render("Tunnel Health"))
	if report.TunnelHealthError != "" {
		lines = append(lines, styles.statusErr.Render(report.TunnelHealthError))
	} else if len(report.TunnelHealth) == 0 {
		lines = append(lines, styles.muted.Render("(none)"))
	} else {
		for _, item := range report.TunnelHealth {
			label := item.AppName
			if item.Provider != "" {
				label += " [" + item.Provider + "]"
			}
			summary := item.Message
			if item.Error != "" {
				summary = item.Error
			}
			if item.SessionSummary != "" {
				summary = strings.TrimSpace(summary + " " + item.SessionSummary)
			}
			lines = append(lines, renderStatusCheckLine(label, summaryOrDefault(summary, item.Status), styles))
		}
	}
	return strings.Join(lines, "\n")
}

func renderStatusCheckLine(label, summary string, styles cliStyles) string {
	return fmt.Sprintf("%s %s", styles.key.Render(label), summary)
}

func serviceSummary(report app.StatusReport) string {
	if report.ServiceError != "" {
		return report.ServiceError
	}
	st := report.Service
	parts := []string{
		"installed=" + boolLabel(st.Installed),
		"running=" + boolLabel(st.Running),
		"ready=" + boolLabel(st.Ready),
	}
	if st.Phase != "" {
		parts = append(parts, "phase="+st.Phase)
	}
	if len(st.MissingEnvVars) > 0 {
		parts = append(parts, "missing_env="+strings.Join(st.MissingEnvVars, ","))
	}
	return strings.Join(parts, "  ")
}

func tlsSummary(tls app.StatusTLSReport) string {
	parts := []string{
		"enabled=" + boolLabel(tls.Enabled),
		"mode=" + tls.Mode,
	}
	if tls.Valid {
		parts = append(parts, "valid=yes")
	} else {
		parts = append(parts, "valid=no")
	}
	if tls.Error != "" {
		parts = append(parts, tls.Error)
	}
	if len(tls.Warnings) > 0 {
		parts = append(parts, strings.Join(tls.Warnings, "; "))
	}
	return strings.Join(parts, "  ")
}

func nextStreamMode(current string) string {
	switch current {
	case "stdout":
		return "stderr"
	case "stderr":
		return "all"
	default:
		return "stdout"
	}
}

func summaryOrDefault(summary, fallback string) string {
	if strings.TrimSpace(summary) != "" {
		return summary
	}
	return fallback
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
