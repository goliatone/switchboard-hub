package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	uiModeAuto  = "auto"
	uiModePlain = "plain"
	uiModeTUI   = "tui"
)

func isInteractiveTTY() bool {
	in, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	out, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (in.Mode()&os.ModeCharDevice) != 0 && (out.Mode()&os.ModeCharDevice) != 0
}

func (r *runContext) uiMode() string {
	mode := strings.ToLower(strings.TrimSpace(r.out.opts.UI))
	if mode == "" {
		return uiModeAuto
	}
	return mode
}

func (r *runContext) isInteractive() bool {
	if r == nil {
		return false
	}
	return r.out.opts.Interactive
}

func (r *runContext) wantsTUI(autoOnInteractive bool) (bool, error) {
	if r == nil || r.out.opts.JSON {
		return false, nil
	}
	switch r.uiMode() {
	case uiModePlain:
		return false, nil
	case uiModeTUI:
		if !r.isInteractive() {
			return false, fmt.Errorf("--ui=tui requires an interactive terminal")
		}
		return true, nil
	default:
		if autoOnInteractive {
			return r.isInteractive(), nil
		}
		return false, nil
	}
}

func (r *runContext) wantsTUIForServiceLog() (bool, error) {
	return r.wantsTUI(true)
}

func (r *runContext) wantsTUIForStatus() (bool, error) {
	return r.wantsTUI(false)
}

func (r *runContext) wantsTUIForAppList() (bool, error) {
	return r.wantsTUI(false)
}

func (r *runContext) wantsTUIForStack() (bool, error) {
	return r.wantsTUI(false)
}

func (r *runContext) wantsTUIForServiceStatus() (bool, error) {
	return r.wantsTUI(false)
}

func (r *runContext) canPromptInteractively() bool {
	return r != nil && !r.out.opts.JSON && r.isInteractive()
}
