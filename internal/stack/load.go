package stack

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func LoadFile(path string) (*Stack, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read stack file %s: %w", path, err)
	}
	st, err := LoadBytes(b)
	if err != nil {
		return nil, err
	}
	st.sourcePath = path
	return st, nil
}

func LoadBytes(data []byte) (*Stack, error) {
	var st Stack
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&st); err != nil {
		return nil, fmt.Errorf("decode stack: %w", err)
	}
	normalizeRawStack(&st)
	if err := st.Validate(); err != nil {
		return nil, err
	}
	return &st, nil
}

func normalizeRawStack(st *Stack) {
	if st == nil {
		return
	}
	st.Name = trimString(st.Name)
	st.Defaults.Provider = trimString(st.Defaults.Provider)
	for i := range st.Services {
		st.Services[i].Name = trimString(st.Services[i].Name)
		st.Services[i].LocalHost = trimString(st.Services[i].LocalHost)
		st.Services[i].PublicHost = trimString(st.Services[i].PublicHost)
		st.Services[i].Provider = trimString(st.Services[i].Provider)
	}
	if len(st.Outputs) == 0 {
		return
	}
	out := make(map[string]string, len(st.Outputs))
	for k, v := range st.Outputs {
		out[trimString(k)] = v
	}
	st.Outputs = out
}

func trimString(raw string) string {
	return string(bytes.TrimSpace([]byte(raw)))
}
