package main

import "github.com/charmbracelet/lipgloss"

type cliStyles struct {
	title       lipgloss.Style
	section     lipgloss.Style
	muted       lipgloss.Style
	key         lipgloss.Style
	tableHeader lipgloss.Style
	tableBorder lipgloss.Style
	badgeOK     lipgloss.Style
	badgeInfo   lipgloss.Style
	badgeWarn   lipgloss.Style
	badgeErr    lipgloss.Style
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
			tableHeader: bold,
			tableBorder: base,
			badgeOK:     bold,
			badgeInfo:   bold,
			badgeWarn:   bold,
			badgeErr:    bold,
			statusOK:    bold,
			statusWarn:  bold,
			statusErr:   bold,
			statusDim:   base,
		}
	}

	return cliStyles{
		title:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")),
		section:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111")),
		muted:       lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		key:         lipgloss.NewStyle().Foreground(lipgloss.Color("180")),
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
		statusOK:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")),
		statusWarn: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")),
		statusErr:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196")),
		statusDim:  lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
}
