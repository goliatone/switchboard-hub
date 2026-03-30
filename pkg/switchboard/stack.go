package switchboard

import (
	"maps"

	internalstack "github.com/goliatone/switchboard-hub/internal/stack"
)

type StackFile struct {
	Version  int               `json:"version" yaml:"version"`
	Name     string            `json:"name" yaml:"name"`
	Defaults StackDefaults     `json:"defaults" yaml:"defaults,omitempty"`
	Services []StackService    `json:"services" yaml:"services"`
	Outputs  map[string]string `json:"outputs,omitempty" yaml:"outputs,omitempty"`
}

type StackDefaults struct {
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Expose   *bool  `json:"expose,omitempty" yaml:"expose,omitempty"`
	Up       *bool  `json:"up,omitempty" yaml:"up,omitempty"`
}

type StackService struct {
	Name       string `json:"name" yaml:"name"`
	LocalHost  string `json:"local_host,omitempty" yaml:"local_host,omitempty"`
	LocalPort  int    `json:"local_port" yaml:"local_port"`
	PublicHost string `json:"public_host,omitempty" yaml:"public_host,omitempty"`
	Provider   string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Expose     *bool  `json:"expose,omitempty" yaml:"expose,omitempty"`
	Up         *bool  `json:"up,omitempty" yaml:"up,omitempty"`
}

type ResolvedStack struct {
	Stack    StackFile              `json:"stack"`
	TLD      string                 `json:"tld"`
	Services []ResolvedStackService `json:"services"`
}

type ResolvedStackService struct {
	Name               string `json:"name"`
	GeneratedAppName   string `json:"generated_app_name"`
	LocalHost          string `json:"local_host"`
	LocalPort          int    `json:"local_port"`
	PublicHost         string `json:"public_host,omitempty"`
	Provider           string `json:"provider,omitempty"`
	Expose             bool   `json:"expose"`
	Up                 bool   `json:"up"`
	OriginalLocalHost  string `json:"original_local_host,omitempty"`
	OriginalPublicHost string `json:"original_public_host,omitempty"`
}

type StackAction struct {
	Type        string `json:"type"`
	Service     string `json:"service,omitempty"`
	Description string `json:"description"`
}

type StackServiceStatus struct {
	Name              string        `json:"name"`
	GeneratedAppName  string        `json:"generated_app_name"`
	LocalHost         string        `json:"local_host"`
	LocalPort         int           `json:"local_port"`
	DesiredPublicHost string        `json:"desired_public_host,omitempty"`
	ActualPublicHost  string        `json:"actual_public_host,omitempty"`
	Provider          string        `json:"provider,omitempty"`
	EndpointID        string        `json:"endpoint_id,omitempty"`
	SessionActive     bool          `json:"session_active"`
	Managed           bool          `json:"managed"`
	Collision         string        `json:"collision,omitempty"`
	Drift             []string      `json:"drift,omitempty"`
	Actions           []StackAction `json:"actions"`
}

type StackManagedOrphan struct {
	AppName    string `json:"app_name"`
	Service    string `json:"service"`
	LocalHost  string `json:"local_host,omitempty"`
	PublicHost string `json:"public_host,omitempty"`
}

type StackReport struct {
	StackName  string               `json:"stack_name"`
	StackFile  string               `json:"stack_file"`
	Services   []StackServiceStatus `json:"services"`
	Orphans    []StackManagedOrphan `json:"orphans,omitempty"`
	HasChanges bool                 `json:"has_changes"`
	HasUnsafe  bool                 `json:"has_unsafe"`
}

func GeneratedAppName(stackName, serviceName string) string {
	return internalstack.GeneratedAppName(stackName, serviceName)
}

func (c *Client) LoadStackFile(path string) (StackFile, error) {
	st, err := internalstack.LoadFile(path)
	if err != nil {
		return StackFile{}, err
	}
	return fromInternalStack(st), nil
}

func (c *Client) ResolveStack(path string) (ResolvedStack, error) {
	resolved, _, _, err := internalstack.LoadResolved(c.service, path)
	if err != nil {
		return ResolvedStack{}, err
	}
	return fromInternalResolvedStack(resolved), nil
}

func (c *Client) RenderStackEnv(path string) ([]string, error) {
	return internalstack.Env(c.service, path)
}

func (c *Client) StackPlan(path string) (StackReport, error) {
	report, err := internalstack.Plan(c.service, path)
	if err != nil {
		return StackReport{}, err
	}
	return fromInternalReport(report), nil
}

func (c *Client) StackStatus(path string) (StackReport, error) {
	report, err := internalstack.Status(c.service, path)
	if err != nil {
		return StackReport{}, err
	}
	return fromInternalReport(report), nil
}

func (c *Client) StackUp(path string) (StackReport, error) {
	report, err := internalstack.Up(c.service, path)
	if report == nil {
		return StackReport{}, err
	}
	return fromInternalReport(report), err
}

func (c *Client) StackDown(path string) (StackReport, error) {
	report, err := internalstack.Down(c.service, path)
	if report == nil {
		return StackReport{}, err
	}
	return fromInternalReport(report), err
}

func fromInternalStack(st *internalstack.Stack) StackFile {
	if st == nil {
		return StackFile{}
	}
	out := StackFile{
		Version: st.Version,
		Name:    st.Name,
		Defaults: StackDefaults{
			Provider: st.Defaults.Provider,
			Expose:   st.Defaults.Expose,
			Up:       st.Defaults.Up,
		},
		Services: make([]StackService, 0, len(st.Services)),
		Outputs:  map[string]string{},
	}
	for _, svc := range st.Services {
		out.Services = append(out.Services, StackService{
			Name:       svc.Name,
			LocalHost:  svc.LocalHost,
			LocalPort:  svc.LocalPort,
			PublicHost: svc.PublicHost,
			Provider:   svc.Provider,
			Expose:     svc.Expose,
			Up:         svc.Up,
		})
	}
	maps.Copy(out.Outputs, st.Outputs)
	return out
}

func fromInternalResolvedStack(st *internalstack.ResolvedStack) ResolvedStack {
	if st == nil {
		return ResolvedStack{}
	}
	out := ResolvedStack{
		Stack:    fromInternalStack(&st.Stack),
		TLD:      st.TLD,
		Services: make([]ResolvedStackService, 0, len(st.Services)),
	}
	for _, svc := range st.Services {
		out.Services = append(out.Services, ResolvedStackService{
			Name:               svc.Name,
			GeneratedAppName:   svc.GeneratedAppName,
			LocalHost:          svc.LocalHost,
			LocalPort:          svc.LocalPort,
			PublicHost:         svc.PublicHost,
			Provider:           svc.Provider,
			Expose:             svc.Expose,
			Up:                 svc.Up,
			OriginalLocalHost:  svc.OriginalLocalHost,
			OriginalPublicHost: svc.OriginalPublicHost,
		})
	}
	return out
}

func fromInternalReport(report *internalstack.Report) StackReport {
	out := StackReport{
		StackName:  report.StackName,
		StackFile:  report.StackFile,
		Services:   make([]StackServiceStatus, 0, len(report.Services)),
		Orphans:    make([]StackManagedOrphan, 0, len(report.Orphans)),
		HasChanges: report.HasChanges,
		HasUnsafe:  report.HasUnsafe,
	}
	for _, svc := range report.Services {
		actions := make([]StackAction, 0, len(svc.Actions))
		for _, action := range svc.Actions {
			actions = append(actions, StackAction{
				Type:        action.Type,
				Service:     action.Service,
				Description: action.Description,
			})
		}
		out.Services = append(out.Services, StackServiceStatus{
			Name:              svc.Name,
			GeneratedAppName:  svc.GeneratedAppName,
			LocalHost:         svc.LocalHost,
			LocalPort:         svc.LocalPort,
			DesiredPublicHost: svc.DesiredPublicHost,
			ActualPublicHost:  svc.ActualPublicHost,
			Provider:          svc.Provider,
			EndpointID:        svc.EndpointID,
			SessionActive:     svc.SessionActive,
			Managed:           svc.Managed,
			Collision:         svc.Collision,
			Drift:             append([]string(nil), svc.Drift...),
			Actions:           actions,
		})
	}
	for _, orphan := range report.Orphans {
		out.Orphans = append(out.Orphans, StackManagedOrphan{
			AppName:    orphan.AppName,
			Service:    orphan.Service,
			LocalHost:  orphan.LocalHost,
			PublicHost: orphan.PublicHost,
		})
	}
	return out
}
