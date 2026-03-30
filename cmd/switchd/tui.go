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

type tuiHelpEntry struct {
	Key   string
	Label string
}

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

type appListTUIModel struct {
	styles      cliStyles
	viewport    viewport.Model
	width       int
	height      int
	ready       bool
	allRows     []appListRow
	visibleRows []appListRow
	selected    int
	filter      string
	filterMode  bool
	healthError string
}

type stackReportTUIModel struct {
	styles   cliStyles
	viewport viewport.Model
	width    int
	height   int
	ready    bool
	model    stackReportViewModel
	selected int
}

type serviceStatusTUIModel struct {
	styles   cliStyles
	viewport viewport.Model
	width    int
	height   int
	ready    bool
	status   app.LaunchdServiceStatus
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

func runStackReportTUI(model stackReportViewModel, styles cliStyles) error {
	tui := &stackReportTUIModel{
		styles: styles,
		model:  model,
	}
	p := tea.NewProgram(tui, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func runServiceStatusTUI(status app.LaunchdServiceStatus, styles cliStyles) error {
	tui := &serviceStatusTUIModel{
		styles: styles,
		status: status,
	}
	p := tea.NewProgram(tui, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func runAppListTUI(model appListViewModel, styles cliStyles) error {
	tui := &appListTUIModel{
		styles:      styles,
		allRows:     append([]appListRow(nil), model.Rows...),
		healthError: model.HealthError,
	}
	tui.applyFilter()
	p := tea.NewProgram(tui, tea.WithAltScreen())
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
			m.viewport = viewport.New(msg.Width, tuiViewportHeight(msg.Height, 8))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = tuiViewportHeight(msg.Height, 8)
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
	statusBits := []string{
		"stream=" + m.stream,
		"follow=" + boolLabel(m.follow),
		"paused=" + boolLabel(m.paused),
	}
	header := renderTUIHeader(m.styles, "switchd service log", statusBits)
	footer := renderTUIFooter(m.styles,
		tuiHelpEntry{Key: "q", Label: "quit"},
		tuiHelpEntry{Key: "p", Label: "pause"},
		tuiHelpEntry{Key: "f", Label: "toggle-follow"},
		tuiHelpEntry{Key: "s", Label: "cycle-stream"},
		tuiHelpEntry{Key: "c", Label: "clear"},
	)
	body := renderTUIState(m.styles, "loading", "Waiting for service logs...")
	if m.ready {
		body = renderTUIPanel(m.styles, "Live Stream", m.viewport.View())
	}
	if m.lastErr != "" {
		body = lipgloss.JoinVertical(lipgloss.Left, renderTUIState(m.styles, "error", m.lastErr), body)
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

func (m *appListTUIModel) Init() tea.Cmd { return nil }

func (m *appListTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		listHeight := tuiViewportHeight(msg.Height, 14)
		if !m.ready {
			m.viewport = viewport.New(msg.Width, listHeight)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = listHeight
		}
		m.refreshViewport()
		return m, nil
	case tea.KeyMsg:
		if m.filterMode {
			switch msg.String() {
			case "esc":
				m.filterMode = false
				return m, nil
			case "enter":
				m.filterMode = false
				return m, nil
			case "backspace":
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
					m.applyFilter()
				}
				return m, nil
			}
			if msg.Type == tea.KeyRunes {
				m.filter += msg.String()
				m.applyFilter()
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "/", "f":
			m.filterMode = true
			return m, nil
		case "esc":
			if m.filter != "" {
				m.filter = ""
				m.applyFilter()
			}
			return m, nil
		case "up", "k":
			if m.selected > 0 {
				m.selected--
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		case "down", "j":
			if m.selected < len(m.visibleRows)-1 {
				m.selected++
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		case "pgup", "b":
			if m.selected > 0 {
				m.selected = max(0, m.selected-m.viewport.Height)
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		case "pgdown", " ":
			if m.selected < len(m.visibleRows)-1 {
				m.selected = min(len(m.visibleRows)-1, m.selected+m.viewport.Height)
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		}
	}
	return m, nil
}

func (m *appListTUIModel) View() string {
	meta := []string{
		fmt.Sprintf("apps=%d", len(m.visibleRows)),
	}
	if m.filter != "" {
		meta = append(meta, "filter="+m.filter)
	}
	if m.filterMode {
		meta = append(meta, "filter-mode=on")
	}
	header := renderTUIHeader(m.styles, "switchd app ls", meta)
	footer := renderTUIFooter(m.styles,
		tuiHelpEntry{Key: "j/k", Label: "move"},
		tuiHelpEntry{Key: "/", Label: "filter"},
		tuiHelpEntry{Key: "esc", Label: "clear-filter"},
		tuiHelpEntry{Key: "q", Label: "quit"},
	)

	listBody := renderTUIState(m.styles, "empty", "No apps match the current filter.")
	if len(m.allRows) == 0 {
		listBody = renderTUIState(m.styles, "empty", "No apps configured.")
	} else if m.ready {
		listBody = renderTUIPanel(m.styles, "Apps", m.viewport.View())
	}
	if m.ready && len(m.visibleRows) > 0 {
		listBody = renderTUIPanel(m.styles, "Apps", m.viewport.View())
	}

	detailBody := renderTUIState(m.styles, "empty", "Select an app to inspect details.")
	if row, ok := m.selectedRow(); ok {
		detailBody = renderTUIPanel(m.styles, "Details", m.renderSelectedDetail(row))
	}

	parts := []string{header}
	if m.healthError != "" {
		parts = append(parts, renderTUIState(m.styles, "error", "Tunnel health lookup: "+m.healthError))
	}
	if m.filterMode {
		parts = append(parts, renderTUIState(m.styles, "loading", "Filter: "+m.filter))
	}
	parts = append(parts, listBody, detailBody, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m *appListTUIModel) applyFilter() {
	needle := strings.ToLower(strings.TrimSpace(m.filter))
	rows := make([]appListRow, 0, len(m.allRows))
	for _, row := range m.allRows {
		if needle == "" || strings.Contains(appListFilterValue(row), needle) {
			rows = append(rows, row)
		}
	}
	m.visibleRows = rows
	if len(m.visibleRows) == 0 {
		m.selected = 0
	} else if m.selected >= len(m.visibleRows) {
		m.selected = len(m.visibleRows) - 1
	}
	m.ensureSelectionVisible()
	m.refreshViewport()
}

func (m *appListTUIModel) ensureSelectionVisible() {
	if !m.ready || len(m.visibleRows) == 0 {
		return
	}
	if m.selected < m.viewport.YOffset {
		m.viewport.YOffset = m.selected
		return
	}
	bottom := m.viewport.YOffset + m.viewport.Height - 1
	if m.selected > bottom {
		m.viewport.YOffset = m.selected - m.viewport.Height + 1
	}
}

func (m *appListTUIModel) refreshViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderListContent())
}

func (m *appListTUIModel) renderListContent() string {
	if len(m.visibleRows) == 0 {
		if len(m.allRows) == 0 {
			return m.styles.empty.Render("No apps configured.")
		}
		return m.styles.empty.Render("No apps match the current filter.")
	}
	lines := make([]string, 0, len(m.visibleRows))
	for i, row := range m.visibleRows {
		line := fmt.Sprintf("%-18s %-22s %-5d %-22s %-8s %s",
			row.Name,
			row.LocalHost,
			row.Port,
			row.PublicHost,
			row.OAuth,
			row.TunnelLabel,
		)
		if i == m.selected {
			line = m.styles.selected.Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *appListTUIModel) selectedRow() (appListRow, bool) {
	if len(m.visibleRows) == 0 || m.selected < 0 || m.selected >= len(m.visibleRows) {
		return appListRow{}, false
	}
	return m.visibleRows[m.selected], true
}

func (m *appListTUIModel) renderSelectedDetail(row appListRow) string {
	lines := []string{
		fmt.Sprintf("%s %s", m.styles.key.Render("name"), row.Name),
		fmt.Sprintf("%s %s:%d", m.styles.key.Render("local"), row.LocalHost, row.Port),
		fmt.Sprintf("%s %s", m.styles.key.Render("public"), row.PublicHost),
		fmt.Sprintf("%s %s", m.styles.key.Render("oauth"), row.OAuth),
		fmt.Sprintf("%s %s %s", m.styles.key.Render("tunnel"), renderStatusChip(m.styles, row.TunnelHealth), row.TunnelLabel),
	}
	if strings.TrimSpace(row.Provider) != "" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("provider"), row.Provider))
	}
	if strings.TrimSpace(row.EndpointHost) != "" && row.EndpointHost != "-" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("endpoint"), row.EndpointHost))
	}
	if strings.TrimSpace(row.SessionID) != "" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("session"), row.SessionID))
	}
	if row.SessionPID > 0 {
		lines = append(lines, fmt.Sprintf("%s %d", m.styles.key.Render("pid"), row.SessionPID))
	}
	if strings.TrimSpace(row.SessionStarted) != "" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("started"), row.SessionStarted))
	}
	if strings.TrimSpace(row.TunnelError) != "" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("error"), row.TunnelError))
	} else if strings.TrimSpace(row.TunnelMessage) != "" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("message"), row.TunnelMessage))
	}
	return strings.Join(lines, "\n")
}

func (m *stackReportTUIModel) Init() tea.Cmd { return nil }

func (m *stackReportTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		listHeight := tuiViewportHeight(msg.Height, 14)
		if !m.ready {
			m.viewport = viewport.New(msg.Width, listHeight)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = listHeight
		}
		m.ensureSelectionVisible()
		m.refreshViewport()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		case "down", "j":
			if m.selected < len(m.model.Rows)-1 {
				m.selected++
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		case "pgup", "b":
			if m.selected > 0 {
				m.selected = max(0, m.selected-m.viewport.Height)
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		case "pgdown", " ":
			if m.selected < len(m.model.Rows)-1 {
				m.selected = min(len(m.model.Rows)-1, m.selected+m.viewport.Height)
				m.ensureSelectionVisible()
				m.refreshViewport()
			}
			return m, nil
		}
	}
	return m, nil
}

func (m *stackReportTUIModel) View() string {
	meta := []string{
		"command=" + valueOrDash(m.model.Command),
		fmt.Sprintf("services=%d", len(m.model.Rows)),
	}
	if m.model.HasChanges {
		meta = append(meta, "changes=yes")
	}
	if m.model.HasUnsafe {
		meta = append(meta, "unsafe=yes")
	}
	header := renderTUIHeader(m.styles, "switchd stack", meta)
	footer := renderTUIFooter(m.styles,
		tuiHelpEntry{Key: "j/k", Label: "move"},
		tuiHelpEntry{Key: "q", Label: "quit"},
	)

	listBody := renderTUIState(m.styles, "empty", "No services in stack report.")
	if m.ready && len(m.model.Rows) > 0 {
		listBody = renderTUIPanel(m.styles, "Services", m.viewport.View())
	}

	detailBody := renderTUIState(m.styles, "empty", "No service selected.")
	if row, ok := m.selectedRow(); ok {
		detailBody = renderTUIPanel(m.styles, "Details", m.renderSelectedDetail(row))
	}

	parts := []string{
		header,
		renderTUIPanel(m.styles, "Stack", m.renderSummary()),
		listBody,
		detailBody,
	}
	if len(m.model.Collisions) > 0 {
		parts = append(parts, renderTUIPanel(m.styles, "Collisions", strings.Join(m.model.Collisions, "\n")))
	}
	if len(m.model.Orphans) > 0 {
		parts = append(parts, renderTUIPanel(m.styles, "Orphans", strings.Join(m.model.Orphans, "\n")))
	}
	parts = append(parts, footer)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m *stackReportTUIModel) renderSummary() string {
	lines := []string{
		fmt.Sprintf("%s %s", m.styles.key.Render("stack"), m.model.StackName),
		fmt.Sprintf("%s %s", m.styles.key.Render("file"), m.model.StackFile),
		fmt.Sprintf("%s %s", m.styles.key.Render("changes"), boolLabel(m.model.HasChanges)),
		fmt.Sprintf("%s %s", m.styles.key.Render("unsafe"), boolLabel(m.model.HasUnsafe)),
	}
	return strings.Join(lines, "\n")
}

func (m *stackReportTUIModel) selectedRow() (stackServiceRow, bool) {
	if len(m.model.Rows) == 0 || m.selected < 0 || m.selected >= len(m.model.Rows) {
		return stackServiceRow{}, false
	}
	return m.model.Rows[m.selected], true
}

func (m *stackReportTUIModel) renderSelectedDetail(row stackServiceRow) string {
	lines := []string{
		fmt.Sprintf("%s %s", m.styles.key.Render("service"), row.Name),
		fmt.Sprintf("%s %s", m.styles.key.Render("app"), row.AppName),
		fmt.Sprintf("%s %s:%d", m.styles.key.Render("local"), row.LocalHost, row.Port),
		fmt.Sprintf("%s %s", m.styles.key.Render("public"), row.PublicHost),
		fmt.Sprintf("%s %s", m.styles.key.Render("provider"), row.Provider),
		fmt.Sprintf("%s %s %s", m.styles.key.Render("session"), renderStatusChip(m.styles, stackSessionHealth(row)), row.Session),
		fmt.Sprintf("%s %s", m.styles.key.Render("managed"), boolLabel(row.Managed)),
		fmt.Sprintf("%s %s", m.styles.key.Render("actions"), strings.Join(row.Actions, ", ")),
	}
	if len(row.Drift) > 0 {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("drift"), strings.Join(row.Drift, ", ")))
	}
	if strings.TrimSpace(row.EndpointID) != "" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("endpoint"), row.EndpointID))
	}
	if strings.TrimSpace(row.Collision) != "" {
		lines = append(lines, fmt.Sprintf("%s %s", m.styles.key.Render("collision"), row.Collision))
	}
	return strings.Join(lines, "\n")
}

func (m *stackReportTUIModel) refreshViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderListContent())
}

func (m *stackReportTUIModel) ensureSelectionVisible() {
	if !m.ready || len(m.model.Rows) == 0 {
		return
	}
	if m.selected < m.viewport.YOffset {
		m.viewport.YOffset = m.selected
		return
	}
	bottom := m.viewport.YOffset + m.viewport.Height - 1
	if m.selected > bottom {
		m.viewport.YOffset = m.selected - m.viewport.Height + 1
	}
}

func (m *stackReportTUIModel) renderListContent() string {
	if len(m.model.Rows) == 0 {
		return m.styles.empty.Render("No services in stack report.")
	}
	lines := make([]string, 0, len(m.model.Rows))
	for i, row := range m.model.Rows {
		drift := "-"
		if len(row.Drift) > 0 {
			drift = strings.Join(row.Drift, ",")
		}
		line := fmt.Sprintf("%-14s %-16s %-20s %-5d %-16s %-10s %-12s",
			row.Name,
			row.AppName,
			row.LocalHost,
			row.Port,
			row.Provider,
			row.Session,
			drift,
		)
		if i == m.selected {
			line = m.styles.selected.Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m *serviceStatusTUIModel) Init() tea.Cmd { return nil }

func (m *serviceStatusTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.viewport = viewport.New(msg.Width, tuiViewportHeight(msg.Height, 8))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = tuiViewportHeight(msg.Height, 8)
		}
		m.viewport.SetContent(renderServiceStatusTUIContent(m.status, m.styles))
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
		if m.ready {
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *serviceStatusTUIModel) View() string {
	header := renderTUIHeader(m.styles, "switchd service status", []string{
		"running=" + boolLabel(m.status.Running),
		"ready=" + boolLabel(m.status.Ready),
	})
	footer := renderTUIFooter(m.styles,
		tuiHelpEntry{Key: "q", Label: "quit"},
	)
	body := renderTUIState(m.styles, "loading", "Loading service status...")
	if m.ready {
		body = renderTUIPanel(m.styles, "Service", m.viewport.View())
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
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
			m.viewport = viewport.New(msg.Width, tuiViewportHeight(msg.Height, 7))
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = tuiViewportHeight(msg.Height, 7)
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
	meta := []string{"r refresh", "q quit"}
	if !m.lastUpdated.IsZero() {
		meta = append(meta, "updated="+m.lastUpdated.Format("15:04:05"))
	}
	header := renderTUIHeader(m.styles, "switchd status", meta)
	footer := renderTUIFooter(m.styles,
		tuiHelpEntry{Key: "r", Label: "refresh"},
		tuiHelpEntry{Key: "q", Label: "quit"},
	)
	body := renderTUIState(m.styles, "loading", "Loading status...")
	if m.ready {
		body = renderTUIPanel(m.styles, "Overview", m.viewport.View())
	}
	if m.lastErr != "" {
		body = lipgloss.JoinVertical(lipgloss.Left, renderTUIState(m.styles, "error", m.lastErr), body)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
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

func renderTUIHeader(styles cliStyles, title string, meta []string) string {
	head := styles.title.Render(title)
	if len(meta) == 0 {
		return head
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, head, "  ", styles.muted.Render(strings.Join(meta, "  ")))
}

func renderTUIFooter(styles cliStyles, entries ...tuiHelpEntry) string {
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Key)
		label := strings.TrimSpace(entry.Label)
		if key == "" && label == "" {
			continue
		}
		switch {
		case key == "":
			parts = append(parts, styles.helpLabel.Render(label))
		case label == "":
			parts = append(parts, styles.helpKey.Render(key))
		default:
			parts = append(parts, styles.helpKey.Render(key)+" "+styles.helpLabel.Render(label))
		}
	}
	return styles.footer.Render(strings.Join(parts, "  "))
}

func renderTUIPanel(styles cliStyles, title, content string) string {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)
	if content == "" {
		content = styles.empty.Render("(empty)")
	}
	body := content
	if title != "" {
		body = lipgloss.JoinVertical(lipgloss.Left, styles.panelTitle.Render(title), content)
	}
	return styles.panel.Render(body)
}

func renderTUIState(styles cliStyles, kind, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "(empty)"
	}
	switch kind {
	case "loading":
		return styles.panel.Render(styles.loading.Render(detail))
	case "empty":
		return styles.panel.Render(styles.empty.Render(detail))
	case "error":
		return styles.errorPanel.Render(detail)
	default:
		return styles.panel.Render(detail)
	}
}

func tuiViewportHeight(totalHeight, reservedLines int) int {
	return max(5, totalHeight-reservedLines)
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
		renderStatusCheckLine("service", serviceSummaryStatus(report), serviceSummary(report), styles),
		renderStatusCheckLine("tls", tlsSummaryStatus(report.TLS), tlsSummary(report.TLS), styles),
		renderStatusCheckLine("dns", report.DNS.Status, report.DNS.Message, styles),
		renderStatusCheckLine("caddy", report.Caddy.Status, report.Caddy.Message, styles),
		"",
		styles.section.Render("Apps"),
	}
	if len(report.Apps) == 0 {
		lines = append(lines, styles.empty.Render("(none)"))
	} else {
		for _, item := range report.Apps {
			lines = append(lines, fmt.Sprintf("%s %s:%d", styles.key.Render(item.Name), item.Host, item.Port))
		}
	}
	lines = append(lines, "", styles.section.Render("Tunnel Health"))
	if report.TunnelHealthError != "" {
		lines = append(lines, styles.chipErr.Render("error")+" "+report.TunnelHealthError)
	} else if len(report.TunnelHealth) == 0 {
		lines = append(lines, styles.empty.Render("(none)"))
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
			lines = append(lines, renderStatusCheckLine(label, item.Status, summaryOrDefault(summary, item.Status), styles))
		}
	}
	return strings.Join(lines, "\n")
}

func renderStatusCheckLine(label, status, summary string, styles cliStyles) string {
	return fmt.Sprintf("%s %s %s", styles.key.Render(label), renderStatusChip(styles, status), summary)
}

func renderStatusChip(styles cliStyles, status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok", "ready":
		return styles.chipOK.Render(status)
	case "info":
		return styles.chipInfo.Render(status)
	case "warning", "warn", "not-ready":
		return styles.chipWarn.Render(status)
	case "error":
		return styles.chipErr.Render(status)
	case "":
		return styles.chipDim.Render("-")
	default:
		return styles.chipDim.Render(status)
	}
}

func serviceSummaryStatus(report app.StatusReport) string {
	if strings.TrimSpace(report.ServiceError) != "" {
		return "error"
	}
	st := report.Service
	switch {
	case st.Running && st.Ready:
		return "ready"
	case st.Stale || strings.TrimSpace(st.StateError) != "":
		return "warning"
	case st.Installed:
		return "info"
	default:
		return "warning"
	}
}

func tlsSummaryStatus(tls app.StatusTLSReport) string {
	switch {
	case !tls.Valid:
		return "error"
	case len(tls.Warnings) > 0:
		return "warning"
	default:
		return "ok"
	}
}

func stackSessionHealth(row stackServiceRow) string {
	switch row.Session {
	case "active":
		return "ok"
	case "collision":
		return "error"
	default:
		if len(row.Drift) > 0 {
			return "warning"
		}
		return "info"
	}
}

func renderServiceStatusTUIContent(st app.LaunchdServiceStatus, styles cliStyles) string {
	lines := []string{
		styles.section.Render("Lifecycle"),
		renderStatusCheckLine("state", serviceStatusHealth(st), serviceStatusSummary(st), styles),
	}
	if st.Phase != "" {
		lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("phase"), st.Phase))
	}
	if st.StartedAt != "" {
		lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("started"), st.StartedAt))
	}

	lines = append(lines, "", styles.section.Render("Processes"))
	if st.PID > 0 {
		lines = append(lines, fmt.Sprintf("%s %d", styles.key.Render("switchd"), st.PID))
	} else {
		lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("switchd"), styles.empty.Render("(not running)")))
	}
	if st.CaddyPID > 0 {
		lines = append(lines, fmt.Sprintf("%s %d", styles.key.Render("caddy"), st.CaddyPID))
	}

	lines = append(lines, "", styles.section.Render("Environment"))
	lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("required"), joinOrNone(st.RequiredEnvVars)))
	lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("configured"), joinOrNone(st.ConfiguredEnvVars)))
	lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("missing"), joinOrNone(st.MissingEnvVars)))

	lines = append(lines, "", styles.section.Render("Paths"))
	lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("plist"), st.PlistPath))
	lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("state"), st.RuntimeStatePath))
	lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("logs"), st.LogDir))
	if st.ConfigPath != "" {
		lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("config"), st.ConfigPath))
	}
	if st.EnvFilePath != "" {
		lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("env"), st.EnvFilePath))
	}

	if st.StateError != "" || len(st.MissingEnvVars) > 0 || (!st.Running && st.Installed) {
		lines = append(lines, "", styles.section.Render("Next Steps"))
		if st.StateError != "" {
			lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("error"), st.StateError))
		}
		if len(st.MissingEnvVars) > 0 && st.EnvFilePath != "" {
			lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("action"), "add missing env vars to "+st.EnvFilePath))
		}
		if !st.Running && st.Installed {
			lines = append(lines, fmt.Sprintf("%s %s", styles.key.Render("start"), "sudo switchd service start"))
		}
	}

	return strings.Join(lines, "\n")
}

func serviceStatusSummary(st app.LaunchdServiceStatus) string {
	parts := []string{
		"installed=" + boolLabel(st.Installed),
		"running=" + boolLabel(st.Running),
		"ready=" + boolLabel(st.Ready),
	}
	if st.Stale {
		parts = append(parts, "stale="+boolLabel(true))
	}
	if len(st.MissingEnvVars) > 0 {
		parts = append(parts, "missing_env="+strings.Join(st.MissingEnvVars, ","))
	}
	return strings.Join(parts, "  ")
}

func serviceStatusHealth(st app.LaunchdServiceStatus) string {
	switch {
	case strings.TrimSpace(st.StateError) != "":
		return "error"
	case st.Running && st.Ready:
		return "ready"
	case st.Stale:
		return "warning"
	case st.Installed:
		return "info"
	default:
		return "warning"
	}
}

func joinOrNone(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
