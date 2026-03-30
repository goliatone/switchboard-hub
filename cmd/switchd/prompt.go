package main

import (
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/goliatone/switchboard-hub/internal/app"
)

var promptServiceEnvValues = defaultPromptServiceEnvValues

func maybeCollectMissingServiceEnv(r *runContext) (app.ServiceEnvironmentReport, error) {
	report, err := prepareServiceEnvRun()
	if err != nil {
		return app.ServiceEnvironmentReport{}, err
	}
	if !r.canPromptInteractively() || len(report.MissingEnvVars) == 0 {
		return report, nil
	}
	values, err := promptServiceEnvValues(report)
	if err != nil {
		return report, err
	}
	if len(values) == 0 {
		return report, nil
	}
	if err := app.SaveServiceEnvValues(report.EnvFilePath, values); err != nil {
		return report, err
	}
	return prepareServiceEnvRun()
}

func defaultPromptServiceEnvValues(report app.ServiceEnvironmentReport) (map[string]string, error) {
	type field struct {
		name  string
		value string
	}
	fields := make([]field, 0, len(report.MissingEnvVars))
	groups := make([]*huh.Group, 0, len(report.MissingEnvVars)+1)
	groups = append(groups, huh.NewGroup(
		huh.NewNote().
			Title("Configure background service credentials").
			Description("Missing values will be written to "+report.EnvFilePath),
	))
	for _, name := range report.MissingEnvVars {
		fields = append(fields, field{name: name})
	}
	for i := range fields {
		input := huh.NewInput().
			Title(fields[i].name).
			Description("Stored in service.env for launchd").
			Value(&fields[i].value)
		if looksSecretEnv(fields[i].name) {
			input = input.EchoMode(huh.EchoModePassword)
		}
		groups = append(groups, huh.NewGroup(input))
	}
	form := huh.NewForm(groups...)
	if err := form.Run(); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, item := range fields {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		out[item.name] = item.value
	}
	return out, nil
}

func looksSecretEnv(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "token") ||
		strings.Contains(name, "secret") ||
		strings.Contains(name, "password") ||
		strings.Contains(name, "key")
}
