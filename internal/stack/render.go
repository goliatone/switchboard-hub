package stack

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

func (r *ResolvedStack) RenderOutputs() (map[string]string, error) {
	if r == nil {
		return nil, fmt.Errorf("resolved stack is nil")
	}
	if len(r.Stack.Outputs) == 0 {
		return map[string]string{}, nil
	}

	out := make(map[string]string, len(r.Stack.Outputs))
	keys := make([]string, 0, len(r.Stack.Outputs))
	for k := range r.Stack.Outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		value, err := r.renderTemplate(strings.TrimSpace(r.Stack.Outputs[key]))
		if err != nil {
			return nil, fmt.Errorf("render output %q: %w", key, err)
		}
		out[key] = value
	}
	return out, nil
}

func (r *ResolvedStack) renderTemplate(raw string) (string, error) {
	tmpl, err := template.New("stack-output").Funcs(r.templateFuncs()).Option("missingkey=error").Parse(raw)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (r *ResolvedStack) templateFuncs() template.FuncMap {
	return template.FuncMap{
		"service": func(name, field string) (string, error) {
			return r.ServiceValue(name, field)
		},
		"parent_domain": func(raw string) (string, error) {
			return parentDomain(raw)
		},
	}
}
