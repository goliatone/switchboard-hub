package main

import "github.com/charmbracelet/lipgloss"

type cliStyles struct {
	title       lipgloss.Style
	section     lipgloss.Style
	muted       lipgloss.Style
	key         lipgloss.Style
	helpKey     lipgloss.Style
	helpLabel   lipgloss.Style
	panel       lipgloss.Style
	panelTitle  lipgloss.Style
	footer      lipgloss.Style
	selected    lipgloss.Style
	loading     lipgloss.Style
	empty       lipgloss.Style
	errorPanel  lipgloss.Style
	tableHeader lipgloss.Style
	tableBorder lipgloss.Style
	badgeOK     lipgloss.Style
	badgeInfo   lipgloss.Style
	badgeWarn   lipgloss.Style
	badgeErr    lipgloss.Style
	chipOK      lipgloss.Style
	chipInfo    lipgloss.Style
	chipWarn    lipgloss.Style
	chipErr     lipgloss.Style
	chipDim     lipgloss.Style
	statusOK    lipgloss.Style
	statusWarn  lipgloss.Style
	statusErr   lipgloss.Style
	statusDim   lipgloss.Style
}

func newCLIStyles(interactive bool) cliStyles {
	if !interactive {
		base := lipgloss.NewStyle()
		bold := base.Bold(true)
		return cliStyles{
			title:       bold,
			section:     bold,
			muted:       base,
			key:         bold,
			helpKey:     bold,
			helpLabel:   base,
			panel:       base,
			panelTitle:  bold,
			footer:      base,
			selected:    bold,
			loading:     base,
			empty:       base,
			errorPanel:  bold,
			tableHeader: bold,
			tableBorder: base,
			badgeOK:     bold,
			badgeInfo:   bold,
			badgeWarn:   bold,
			badgeErr:    bold,
			chipOK:      bold,
			chipInfo:    bold,
			chipWarn:    bold,
			chipErr:     bold,
			chipDim:     base,
			statusOK:    bold,
			statusWarn:  bold,
			statusErr:   bold,
			statusDim:   base,
		}
	}

	return cliStyles{
		title:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")),
		section:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111")),
		muted:      lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		key:        lipgloss.NewStyle().Foreground(lipgloss.Color("180")),
		helpKey:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("222")),
		helpLabel:  lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		panel:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("239")).Padding(0, 1),
		panelTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("153")),
		footer:     lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		selected:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62")),
		loading:    lipgloss.NewStyle().Foreground(lipgloss.Color("221")),
		empty:      lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true),
		errorPanel: lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("160")).
			Padding(0, 1),
		tableHeader: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")),
		tableBorder: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		badgeOK: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("35")).
			Padding(0, 1),
		badgeInfo: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("33")).
			Padding(0, 1),
		badgeWarn: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("235")).
			Background(lipgloss.Color("214")).
			Padding(0, 1),
		badgeErr: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("196")).
			Padding(0, 1),
		chipOK: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("42")),
		chipInfo: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("45")),
		chipWarn: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("214")),
		chipErr: lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("196")),
		chipDim:    lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		statusOK:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")),
		statusWarn: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")),
		statusErr:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")),
		statusDim:  lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
}
