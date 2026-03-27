package stack

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
)

const (
	metadataManagedBy = "managed_by"
	metadataStack     = "stack"
	metadataService   = "service"
	metadataStackFile = "stack_file"
)

type Action struct {
	Type        string `json:"type"`
	Service     string `json:"service,omitempty"`
	Description string `json:"description"`
}

type ServiceStatus struct {
	Name              string   `json:"name"`
	GeneratedAppName  string   `json:"generated_app_name"`
	LocalHost         string   `json:"local_host"`
	LocalPort         int      `json:"local_port"`
	DesiredPublicHost string   `json:"desired_public_host,omitempty"`
	ActualPublicHost  string   `json:"actual_public_host,omitempty"`
	Provider          string   `json:"provider,omitempty"`
	EndpointID        string   `json:"endpoint_id,omitempty"`
	SessionActive     bool     `json:"session_active"`
	Managed           bool     `json:"managed"`
	Collision         string   `json:"collision,omitempty"`
	Drift             []string `json:"drift,omitempty"`
	Actions           []Action `json:"actions"`
}

type ManagedOrphan struct {
	AppName    string `json:"app_name"`
	Service    string `json:"service"`
	LocalHost  string `json:"local_host,omitempty"`
	PublicHost string `json:"public_host,omitempty"`
}

type Report struct {
	StackName  string          `json:"stack_name"`
	StackFile  string          `json:"stack_file"`
	Services   []ServiceStatus `json:"services"`
	Orphans    []ManagedOrphan `json:"orphans,omitempty"`
	HasChanges bool            `json:"has_changes"`
	HasUnsafe  bool            `json:"has_unsafe"`
}

type managedAppUpdate struct {
	Changed        bool
	PreviousHost   string
	CurrentAppName string
}

func LoadResolved(service *app.Service, path string) (*ResolvedStack, *config.Config, string, error) {
	st, err := LoadFile(path)
	if err != nil {
		return nil, nil, "", err
	}
	configPath, cfg, err := service.LoadOrDefaultConfig()
	if err != nil {
		return nil, nil, "", err
	}
	resolved, err := st.Resolve(cfg.TLD)
	if err != nil {
		return nil, nil, "", err
	}
	return resolved, cfg, configPath, nil
}

func Plan(service *app.Service, path string) (*Report, error) {
	resolved, cfg, _, err := LoadResolved(service, path)
	if err != nil {
		return nil, err
	}
	report := buildReport(resolved, cfg, path)
	return report, nil
}

func Status(service *app.Service, path string) (*Report, error) {
	return Plan(service, path)
}

func Env(service *app.Service, path string) ([]string, error) {
	resolved, _, _, err := LoadResolved(service, path)
	if err != nil {
		return nil, err
	}
	return resolved.RenderEnvLines()
}

func Up(service *app.Service, path string) (*Report, error) {
	resolved, cfg, configPath, err := LoadResolved(service, path)
	if err != nil {
		return nil, err
	}
	planned := buildReport(resolved, cfg, path)
	if planned.HasUnsafe {
		return planned, unsafeReportError(planned)
	}

	appOrRouteChanged := false
	for _, svc := range resolved.Services {
		status := findServiceStatus(planned, svc.Name)
		if status != nil && status.Collision != "" {
			continue
		}
		update := ensureManagedApp(cfg, resolved, svc, path)
		if update.Changed {
			appOrRouteChanged = true
		}
		if removeRouteIfUnused(cfg, update.PreviousHost) {
			appOrRouteChanged = true
		}
		if ensureRoute(cfg, svc.LocalHost, svc.LocalPort) {
			appOrRouteChanged = true
		}
	}
	if appOrRouteChanged {
		if err := service.SaveConfigAt(configPath, cfg); err != nil {
			return nil, err
		}
		if err := service.ApplyConfig(configPath, cfg); err != nil {
			return nil, err
		}
	}

	endpointChanged := false
	for _, svc := range resolved.Services {
		if !svc.Expose {
			continue
		}
		status := findServiceStatus(planned, svc.Name)
		if status != nil && status.Collision != "" {
			continue
		}
		if needsExpose(status) {
			if _, err := service.EnsurePublicEndpoint(cfg, svc.GeneratedAppName, svc.Provider, svc.PublicHost); err != nil {
				return nil, err
			}
			endpointChanged = true
		}
	}
	if endpointChanged {
		if err := service.SaveConfigAt(configPath, cfg); err != nil {
			return nil, err
		}
	}

	sessionChanged := false
	for _, svc := range resolved.Services {
		if !svc.Up {
			continue
		}
		status := findServiceStatus(planned, svc.Name)
		if status != nil && status.Collision != "" {
			continue
		}
		if needsStart(status) {
			if _, err := service.EnsureAppRuntime(cfg, svc.GeneratedAppName); err != nil {
				return nil, err
			}
			sessionChanged = true
		}
	}
	if sessionChanged {
		if err := service.SaveConfigAt(configPath, cfg); err != nil {
			return nil, err
		}
	}

	return mergeReportActions(planned, buildReport(resolved, cfg, path)), nil
}

func Down(service *app.Service, path string) (*Report, error) {
	resolved, cfg, configPath, err := LoadResolved(service, path)
	if err != nil {
		return nil, err
	}
	planned := buildReport(resolved, cfg, path)

	changed := false
	for _, svc := range resolved.Services {
		idx, actual, err := managedAppForService(cfg, resolved.Stack.Name, svc.Name)
		if err != nil {
			return planned, err
		}
		if idx < 0 || strings.TrimSpace(actual.PublicEndpoint.ActiveSessionID) == "" {
			continue
		}
		if _, err := service.StopAppRuntime(cfg, actual.Name); err != nil {
			return nil, err
		}
		changed = true
	}
	if changed {
		if err := service.SaveConfigAt(configPath, cfg); err != nil {
			return nil, err
		}
	}
	final := buildReport(resolved, cfg, path)
	for i := range planned.Services {
		if planned.Services[i].SessionActive {
			planned.HasChanges = true
			planned.Services[i].Actions = []Action{{Type: "stop_tunnel", Service: planned.Services[i].Name, Description: "stop runtime session"}}
		}
	}
	return mergeReportActions(planned, final), nil
}

func buildReport(resolved *ResolvedStack, cfg *config.Config, stackFile string) *Report {
	report := &Report{
		StackName: resolved.Stack.Name,
		StackFile: stackFile,
		Services:  make([]ServiceStatus, 0, len(resolved.Services)),
		Orphans:   findOrphans(resolved, cfg),
	}
	for _, svc := range resolved.Services {
		status := inspectService(resolved, cfg, svc)
		if len(status.Actions) == 0 {
			status.Actions = []Action{{Type: "no_op", Service: svc.Name, Description: "no-op"}}
		}
		if status.Collision != "" {
			report.HasUnsafe = true
		}
		for _, action := range status.Actions {
			if action.Type != "no_op" {
				report.HasChanges = true
				break
			}
		}
		report.Services = append(report.Services, status)
	}
	sort.Slice(report.Services, func(i, j int) bool { return report.Services[i].Name < report.Services[j].Name })
	sort.Slice(report.Orphans, func(i, j int) bool { return report.Orphans[i].AppName < report.Orphans[j].AppName })
	return report
}

func inspectService(resolved *ResolvedStack, cfg *config.Config, svc ResolvedService) ServiceStatus {
	status := ServiceStatus{
		Name:              svc.Name,
		GeneratedAppName:  svc.GeneratedAppName,
		LocalHost:         svc.LocalHost,
		LocalPort:         svc.LocalPort,
		DesiredPublicHost: svc.PublicHost,
		Provider:          desiredProvider(cfg, svc),
	}
	managedIndexes := findManagedIndexes(cfg, resolved.Stack.Name, svc.Name)
	if len(managedIndexes) > 1 {
		status.Collision = fmt.Sprintf("multiple stack-managed apps found for %s/%s", resolved.Stack.Name, svc.Name)
		status.Actions = append(status.Actions, Action{Type: "collision", Service: svc.Name, Description: status.Collision})
		return status
	}

	if len(managedIndexes) == 1 {
		managedIndex := managedIndexes[0]
		if collision := managedCollision(cfg, managedIndex, svc); collision != "" {
			status.Collision = collision
			status.Actions = append(status.Actions, Action{Type: "collision", Service: svc.Name, Description: collision})
			return status
		}
		actual := cfg.Apps[managedIndex]
		status.Managed = true
		populateActual(&status, actual)
		addManagedDrift(&status, cfg, svc, actual)
		return status
	}

	if collision := unrelatedCollision(cfg, svc); collision != "" {
		status.Collision = collision
		status.Actions = append(status.Actions, Action{Type: "collision", Service: svc.Name, Description: collision})
		return status
	}

	status.Drift = append(status.Drift, "missing_app")
	status.Actions = append(status.Actions, Action{Type: "create_app", Service: svc.Name, Description: fmt.Sprintf("create app %s", svc.GeneratedAppName)})
	status.Actions = append(status.Actions, Action{Type: "ensure_route", Service: svc.Name, Description: fmt.Sprintf("ensure route %s -> 127.0.0.1:%d", svc.LocalHost, svc.LocalPort)})
	if svc.Expose {
		status.Actions = append(status.Actions, Action{Type: "expose_endpoint", Service: svc.Name, Description: "expose public endpoint"})
	}
	if shouldManageSession(svc) {
		status.Actions = append(status.Actions, Action{Type: "start_tunnel", Service: svc.Name, Description: "start runtime session"})
	}
	return status
}

func populateActual(status *ServiceStatus, actual config.App) {
	status.ActualPublicHost = actual.PublicEndpoint.Host
	if status.Provider == "" {
		status.Provider = actual.PublicEndpoint.Provider
	}
	status.EndpointID = actual.PublicEndpoint.EndpointID
	status.SessionActive = strings.TrimSpace(actual.PublicEndpoint.ActiveSessionID) != ""
}

func addManagedDrift(status *ServiceStatus, cfg *config.Config, svc ResolvedService, actual config.App) {
	if actual.Name != svc.GeneratedAppName {
		status.Drift = append(status.Drift, "app_name")
		status.Actions = append(status.Actions, Action{Type: "update_app_name", Service: svc.Name, Description: fmt.Sprintf("rename app to %s", svc.GeneratedAppName)})
	}
	if actual.LocalHost != svc.LocalHost {
		status.Drift = append(status.Drift, "local_host")
		status.Actions = append(status.Actions, Action{Type: "update_local_host", Service: svc.Name, Description: fmt.Sprintf("set local host to %s", svc.LocalHost)})
	}
	if actual.LocalPort != svc.LocalPort {
		status.Drift = append(status.Drift, "local_port")
		status.Actions = append(status.Actions, Action{Type: "update_app_port", Service: svc.Name, Description: fmt.Sprintf("set local port to %d", svc.LocalPort)})
	}
	if !routeMatches(cfg, svc.LocalHost, svc.LocalPort) {
		status.Drift = append(status.Drift, "route")
		status.Actions = append(status.Actions, Action{Type: "ensure_route", Service: svc.Name, Description: fmt.Sprintf("ensure route %s -> 127.0.0.1:%d", svc.LocalHost, svc.LocalPort)})
	}
	if svc.Expose {
		if status.Provider == "" {
			status.Drift = append(status.Drift, "provider")
			status.Actions = append(status.Actions, Action{Type: "expose_endpoint", Service: svc.Name, Description: "expose public endpoint"})
		} else if status.Provider != actual.PublicEndpoint.Provider {
			status.Drift = append(status.Drift, "provider")
			status.Actions = append(status.Actions, Action{Type: "expose_endpoint", Service: svc.Name, Description: "reconcile provider/endpoint"})
		}
		if svc.PublicHost != "" && actual.PublicEndpoint.Host != svc.PublicHost {
			status.Drift = append(status.Drift, "public_host")
			status.Actions = append(status.Actions, Action{Type: "expose_endpoint", Service: svc.Name, Description: "reconcile public endpoint"})
		}
		if strings.TrimSpace(actual.PublicEndpoint.EndpointID) == "" {
			status.Drift = append(status.Drift, "endpoint")
			status.Actions = append(status.Actions, Action{Type: "expose_endpoint", Service: svc.Name, Description: "create public endpoint"})
		}
	}
	if shouldManageSession(svc) && strings.TrimSpace(actual.PublicEndpoint.ActiveSessionID) == "" {
		status.Drift = append(status.Drift, "session")
		status.Actions = append(status.Actions, Action{Type: "start_tunnel", Service: svc.Name, Description: "start runtime session"})
	}
}

func findManagedIndexes(cfg *config.Config, stackName, serviceName string) []int {
	out := []int{}
	for i, candidate := range cfg.Apps {
		if isManagedApp(candidate, stackName, serviceName) {
			out = append(out, i)
		}
	}
	return out
}

func isManagedApp(candidate config.App, stackName, serviceName string) bool {
	if !strings.EqualFold(candidate.Metadata[metadataManagedBy], "stack") {
		return false
	}
	if !strings.EqualFold(candidate.Metadata[metadataStack], stackName) {
		return false
	}
	if !strings.EqualFold(candidate.Metadata[metadataService], serviceName) {
		return false
	}
	return true
}

func unrelatedCollision(cfg *config.Config, svc ResolvedService) string {
	for _, candidate := range cfg.Apps {
		if strings.EqualFold(candidate.Name, svc.GeneratedAppName) {
			return fmt.Sprintf("generated app name %q collides with unrelated app %q", svc.GeneratedAppName, candidate.Name)
		}
		if strings.EqualFold(candidate.LocalHost, svc.LocalHost) {
			return fmt.Sprintf("local host %q collides with unrelated app %q", svc.LocalHost, candidate.Name)
		}
		if svc.PublicHost != "" && strings.EqualFold(candidate.PublicEndpoint.Host, svc.PublicHost) {
			return fmt.Sprintf("public host %q collides with unrelated app %q", svc.PublicHost, candidate.Name)
		}
	}
	return ""
}

func managedCollision(cfg *config.Config, managedIndex int, svc ResolvedService) string {
	for i, candidate := range cfg.Apps {
		if i == managedIndex {
			continue
		}
		if strings.EqualFold(candidate.Name, svc.GeneratedAppName) {
			return fmt.Sprintf("generated app name %q collides with unrelated app %q", svc.GeneratedAppName, candidate.Name)
		}
		if strings.EqualFold(candidate.LocalHost, svc.LocalHost) {
			return fmt.Sprintf("local host %q collides with unrelated app %q", svc.LocalHost, candidate.Name)
		}
		if svc.PublicHost != "" && strings.EqualFold(candidate.PublicEndpoint.Host, svc.PublicHost) {
			return fmt.Sprintf("public host %q collides with unrelated app %q", svc.PublicHost, candidate.Name)
		}
	}
	return ""
}

func desiredProvider(cfg *config.Config, svc ResolvedService) string {
	if strings.TrimSpace(svc.Provider) != "" {
		return strings.TrimSpace(svc.Provider)
	}
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Tunnel.DefaultProvider)
}

func routeMatches(cfg *config.Config, host string, port int) bool {
	dial := "127.0.0.1:" + strconv.Itoa(port)
	for _, route := range cfg.Routes {
		if strings.EqualFold(route.Host, host) {
			return route.Dial == dial
		}
	}
	return false
}

func ensureRoute(cfg *config.Config, host string, port int) bool {
	dial := "127.0.0.1:" + strconv.Itoa(port)
	for i := range cfg.Routes {
		if strings.EqualFold(cfg.Routes[i].Host, host) {
			if cfg.Routes[i].Dial == dial {
				return false
			}
			cfg.Routes[i].Dial = dial
			sort.Slice(cfg.Routes, func(i, j int) bool { return cfg.Routes[i].Host < cfg.Routes[j].Host })
			return true
		}
	}
	cfg.Routes = append(cfg.Routes, config.Route{Host: host, Dial: dial})
	sort.Slice(cfg.Routes, func(i, j int) bool { return cfg.Routes[i].Host < cfg.Routes[j].Host })
	return true
}

func removeRouteIfUnused(cfg *config.Config, host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	for _, candidate := range cfg.Apps {
		if strings.EqualFold(candidate.LocalHost, host) {
			return false
		}
	}
	out := make([]config.Route, 0, len(cfg.Routes))
	removed := false
	for _, route := range cfg.Routes {
		if strings.EqualFold(route.Host, host) {
			removed = true
			continue
		}
		out = append(out, route)
	}
	if removed {
		cfg.Routes = out
	}
	return removed
}

func ensureManagedApp(cfg *config.Config, resolved *ResolvedStack, svc ResolvedService, stackFile string) managedAppUpdate {
	indexes := findManagedIndexes(cfg, resolved.Stack.Name, svc.Name)
	meta := map[string]string{
		metadataManagedBy: "stack",
		metadataStack:     resolved.Stack.Name,
		metadataService:   svc.Name,
		metadataStackFile: stackFile,
	}
	if len(indexes) == 1 {
		idx := indexes[0]
		update := managedAppUpdate{
			CurrentAppName: cfg.Apps[idx].Name,
		}
		if cfg.Apps[idx].LocalHost != svc.LocalHost {
			update.PreviousHost = cfg.Apps[idx].LocalHost
		}
		if cfg.Apps[idx].Name != svc.GeneratedAppName {
			cfg.Apps[idx].Name = svc.GeneratedAppName
			update.Changed = true
		}
		if cfg.Apps[idx].LocalHost != svc.LocalHost {
			cfg.Apps[idx].LocalHost = svc.LocalHost
			update.Changed = true
		}
		if cfg.Apps[idx].LocalPort != svc.LocalPort {
			cfg.Apps[idx].LocalPort = svc.LocalPort
			update.Changed = true
		}
		if cfg.Apps[idx].Metadata == nil {
			cfg.Apps[idx].Metadata = map[string]string{}
		}
		for k, v := range meta {
			if cfg.Apps[idx].Metadata[k] != v {
				cfg.Apps[idx].Metadata[k] = v
				update.Changed = true
			}
		}
		sort.Slice(cfg.Apps, func(i, j int) bool { return cfg.Apps[i].Name < cfg.Apps[j].Name })
		update.CurrentAppName = svc.GeneratedAppName
		return update
	}

	cfg.Apps = append(cfg.Apps, config.App{
		Name:      svc.GeneratedAppName,
		LocalHost: svc.LocalHost,
		LocalPort: svc.LocalPort,
		Metadata:  meta,
	})
	sort.Slice(cfg.Apps, func(i, j int) bool { return cfg.Apps[i].Name < cfg.Apps[j].Name })
	return managedAppUpdate{
		Changed:        true,
		CurrentAppName: svc.GeneratedAppName,
	}
}

func findOrphans(resolved *ResolvedStack, cfg *config.Config) []ManagedOrphan {
	desired := map[string]struct{}{}
	for _, svc := range resolved.Services {
		desired[normalizeLookupKey(svc.Name)] = struct{}{}
	}
	out := []ManagedOrphan{}
	for _, candidate := range cfg.Apps {
		if !strings.EqualFold(candidate.Metadata[metadataManagedBy], "stack") {
			continue
		}
		if !strings.EqualFold(candidate.Metadata[metadataStack], resolved.Stack.Name) {
			continue
		}
		serviceName := normalizeLookupKey(candidate.Metadata[metadataService])
		if _, ok := desired[serviceName]; ok {
			continue
		}
		out = append(out, ManagedOrphan{
			AppName:    candidate.Name,
			Service:    candidate.Metadata[metadataService],
			LocalHost:  candidate.LocalHost,
			PublicHost: candidate.PublicEndpoint.Host,
		})
	}
	return out
}

func findServiceStatus(report *Report, name string) *ServiceStatus {
	for i := range report.Services {
		if strings.EqualFold(report.Services[i].Name, name) {
			return &report.Services[i]
		}
	}
	return nil
}

func managedAppForService(cfg *config.Config, stackName, serviceName string) (int, config.App, error) {
	indexes := findManagedIndexes(cfg, stackName, serviceName)
	if len(indexes) > 1 {
		return -1, config.App{}, fmt.Errorf("ambiguous stack-managed apps for %s/%s", stackName, serviceName)
	}
	if len(indexes) == 0 {
		return -1, config.App{}, nil
	}
	return indexes[0], cfg.Apps[indexes[0]], nil
}

func needsExpose(status *ServiceStatus) bool {
	if status == nil {
		return false
	}
	for _, action := range status.Actions {
		if action.Type == "expose_endpoint" {
			return true
		}
	}
	return false
}

func needsStart(status *ServiceStatus) bool {
	if status == nil {
		return false
	}
	for _, action := range status.Actions {
		if action.Type == "start_tunnel" {
			return true
		}
	}
	return false
}

func shouldManageSession(svc ResolvedService) bool {
	return svc.Expose && svc.Up
}

func unsafeReportError(report *Report) error {
	issues := []string{}
	for _, svc := range report.Services {
		if svc.Collision != "" {
			issues = append(issues, fmt.Sprintf("%s: %s", svc.Name, svc.Collision))
		}
	}
	return fmt.Errorf("unsafe stack reconciliation: %s", strings.Join(issues, "; "))
}

func mergeReportActions(planned, final *Report) *Report {
	if planned == nil {
		return final
	}
	if final == nil {
		return planned
	}
	final.HasChanges = planned.HasChanges
	final.HasUnsafe = planned.HasUnsafe || final.HasUnsafe
	byName := map[string]ServiceStatus{}
	for _, svc := range planned.Services {
		byName[normalizeLookupKey(svc.Name)] = svc
	}
	for i := range final.Services {
		key := normalizeLookupKey(final.Services[i].Name)
		plannedSvc, ok := byName[key]
		if !ok {
			continue
		}
		if hasNonNoop(plannedSvc.Actions) {
			final.Services[i].Actions = plannedSvc.Actions
			final.Services[i].Drift = plannedSvc.Drift
		}
		if final.Services[i].Collision == "" {
			final.Services[i].Collision = plannedSvc.Collision
		}
	}
	return final
}

func hasNonNoop(actions []Action) bool {
	for _, action := range actions {
		if action.Type != "no_op" {
			return true
		}
	}
	return false
}
