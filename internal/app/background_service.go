package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
)

const (
	launchdServiceLabel    = "com.goliatone.switchd"
	launchdServiceDomain   = "system"
	serviceReadyTimeout    = 30 * time.Second
	serviceStopTimeout     = 90 * time.Second
	serviceLogPollInterval = 250 * time.Millisecond
)

var (
	launchdPlistPath   = "/Library/LaunchDaemons/" + launchdServiceLabel + ".plist"
	launchdRuntimeDir  = "/var/run/switchboard-hub"
	launchdLogDir      = "/var/log/switchboard-hub"
	serviceStatePath   = filepath.Join(launchdRuntimeDir, "daemon-state.json")
	serviceLockPath    = filepath.Join(launchdRuntimeDir, "daemon.lock")
	serviceStdoutPath  = filepath.Join(launchdLogDir, "switchd.stdout.log")
	serviceStderrPath  = filepath.Join(launchdLogDir, "switchd.stderr.log")
	launchctlRun       = defaultLaunchctlRun
	resolveExecutable  = defaultResolveExecutable
	waitForCaddyAdmin  = defaultWaitForCaddyAdmin
	startBackgroundCmd = defaultStartBackgroundCommand
	effectiveUID       = os.Geteuid
)

type LaunchdServiceStatus struct {
	Label             string   `json:"label"`
	PlistPath         string   `json:"plist_path"`
	RuntimeStatePath  string   `json:"runtime_state_path"`
	LogDir            string   `json:"log_dir"`
	EnvFilePath       string   `json:"env_file_path,omitempty"`
	ConfigPath        string   `json:"config_path,omitempty"`
	Installed         bool     `json:"installed"`
	Running           bool     `json:"running"`
	Ready             bool     `json:"ready"`
	Stale             bool     `json:"stale"`
	PID               int      `json:"pid,omitempty"`
	CaddyPID          int      `json:"caddy_pid,omitempty"`
	StartedAt         string   `json:"started_at,omitempty"`
	Phase             string   `json:"phase,omitempty"`
	StateError        string   `json:"state_error,omitempty"`
	RequiredEnvVars   []string `json:"required_env_vars,omitempty"`
	MissingEnvVars    []string `json:"missing_env_vars,omitempty"`
	ConfiguredEnvVars []string `json:"configured_env_vars,omitempty"`
}

type ServiceEnvironmentReport struct {
	ConfigPath          string   `json:"config_path"`
	EnvFilePath         string   `json:"env_file_path"`
	RequiredEnvVars     []string `json:"required_env_vars,omitempty"`
	ConfiguredEnvVars   []string `json:"configured_env_vars,omitempty"`
	MissingEnvVars      []string `json:"missing_env_vars,omitempty"`
	EnvFileCreated      bool     `json:"env_file_created,omitempty"`
	EnvFileUpdated      bool     `json:"env_file_updated,omitempty"`
	EnvFileTemplateVars []string `json:"env_file_template_vars,omitempty"`
}

type daemonResumeReport struct {
	ActiveApps  int
	FailedApps  []string
	ResumedApps int
}

type daemonRuntimeState struct {
	PID        int    `json:"pid"`
	CaddyPID   int    `json:"caddy_pid,omitempty"`
	StartedAt  string `json:"started_at"`
	ConfigPath string `json:"config_path"`
	Ready      bool   `json:"ready"`
	Phase      string `json:"phase,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}

type daemonProcess interface {
	PID() int
	Kill() error
	Signal(os.Signal) error
	Wait() error
}

type execBackgroundProcess struct {
	cmd      *exec.Cmd
	waitOnce sync.Once
	waitDone chan struct{}
	waitErr  error
	waitMu   sync.RWMutex
}

func (p *execBackgroundProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *execBackgroundProcess) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *execBackgroundProcess) Signal(sig os.Signal) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Signal(sig)
}

func (p *execBackgroundProcess) Wait() error {
	if p == nil || p.cmd == nil {
		return nil
	}
	p.waitOnce.Do(func() {
		p.waitDone = make(chan struct{})
		go func() {
			err := p.cmd.Wait()
			p.waitMu.Lock()
			p.waitErr = err
			p.waitMu.Unlock()
			close(p.waitDone)
		}()
	})
	<-p.waitDone
	p.waitMu.RLock()
	defer p.waitMu.RUnlock()
	return p.waitErr
}

type daemonLock struct {
	file *os.File
}

type backgroundDaemon struct {
	service    *Service
	configPath string
	now        func() time.Time
}

type ServiceLogOptions struct {
	Lines  int
	Follow bool
	Stream string
	JSON   bool
	Stdout io.Writer
	Stderr io.Writer
}

type serviceLogStream struct {
	name    string
	path    string
	writer  io.Writer
	json    bool
	stat    os.FileInfo
	offset  int64
	partial string
}

type serviceLogEvent struct {
	Stream string `json:"stream"`
	Line   string `json:"line"`
}

func launchdServiceTarget() string {
	return launchdServiceDomain + "/" + launchdServiceLabel
}

func ServiceLog(opts ServiceLogOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serviceLogWithContext(ctx, opts)
}

func serviceLogWithContext(ctx context.Context, opts ServiceLogOptions) error {
	streams, prefixLabels, err := newServiceLogStreams(opts)
	if err != nil {
		return err
	}

	lines := opts.Lines
	if lines < 0 {
		return fmt.Errorf("--lines must be >= 0")
	}

	found := false
	for _, stream := range streams {
		exists, err := stream.snapshot(lines, prefixLabels)
		if err != nil {
			return err
		}
		if exists {
			found = true
		}
	}
	if !found && !opts.Follow {
		paths := make([]string, 0, len(streams))
		for _, stream := range streams {
			paths = append(paths, stream.path)
		}
		sort.Strings(paths)
		return fmt.Errorf(
			"service logs not found (%s). Check `switchd service status`, then run `sudo switchd service install` or `sudo switchd service start`",
			strings.Join(paths, ", "),
		)
	}
	if !opts.Follow {
		return nil
	}

	ticker := time.NewTicker(serviceLogPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for _, stream := range streams {
				if err := stream.poll(prefixLabels); err != nil {
					return err
				}
			}
		}
	}
}

func newServiceLogStreams(opts ServiceLogOptions) ([]*serviceLogStream, bool, error) {
	mode := strings.ToLower(strings.TrimSpace(opts.Stream))
	if mode == "" {
		mode = "all"
	}

	stdoutWriter := opts.Stdout
	if stdoutWriter == nil {
		stdoutWriter = os.Stdout
	}
	stderrWriter := opts.Stderr
	if stderrWriter == nil {
		stderrWriter = os.Stderr
	}

	streams := make([]*serviceLogStream, 0, 2)
	switch mode {
	case "all":
		streams = append(streams,
			&serviceLogStream{name: "stdout", path: serviceStdoutPath, writer: stdoutWriter, json: opts.JSON},
			&serviceLogStream{name: "stderr", path: serviceStderrPath, writer: stderrWriter, json: opts.JSON},
		)
	case "stdout":
		streams = append(streams, &serviceLogStream{name: "stdout", path: serviceStdoutPath, writer: stdoutWriter, json: opts.JSON})
	case "stderr":
		streams = append(streams, &serviceLogStream{name: "stderr", path: serviceStderrPath, writer: stderrWriter, json: opts.JSON})
	default:
		return nil, false, fmt.Errorf("unsupported stream %q", opts.Stream)
	}

	return streams, len(streams) > 1 && !opts.JSON, nil
}

func (s *serviceLogStream) snapshot(lines int, prefix bool) (bool, error) {
	data, stat, err := readServiceLogFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.stat = nil
			s.offset = 0
			s.partial = ""
			return false, nil
		}
		return false, err
	}

	s.stat = stat
	s.offset = int64(len(data))
	s.partial = ""

	for _, line := range tailLines(data, lines) {
		if err := s.writeLine(line, prefix); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (s *serviceLogStream) poll(prefix bool) error {
	info, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.stat = nil
			s.offset = 0
			s.partial = ""
			return nil
		}
		return err
	}

	if s.stat != nil && !os.SameFile(s.stat, info) {
		s.offset = 0
		s.partial = ""
	}
	if info.Size() < s.offset {
		s.offset = 0
		s.partial = ""
	}
	if info.Size() == s.offset {
		s.stat = info
		return nil
	}

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.stat = nil
			s.offset = 0
			s.partial = ""
			return nil
		}
		return err
	}
	defer f.Close()

	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return err
	}

	chunk, err := io.ReadAll(f)
	if err != nil {
		return err
	}

	s.offset += int64(len(chunk))
	s.stat = info
	return s.consume(chunk, prefix)
}

func (s *serviceLogStream) consume(chunk []byte, prefix bool) error {
	if len(chunk) == 0 {
		return nil
	}

	data := s.partial + string(chunk)
	for {
		idx := strings.IndexByte(data, '\n')
		if idx < 0 {
			s.partial = data
			return nil
		}
		line := strings.TrimSuffix(data[:idx], "\r")
		if err := s.writeLine(line, prefix); err != nil {
			return err
		}
		data = data[idx+1:]
	}
}

func (s *serviceLogStream) writeLine(line string, prefix bool) error {
	if s == nil || s.writer == nil {
		return nil
	}
	if s.json {
		return json.NewEncoder(s.writer).Encode(serviceLogEvent{
			Stream: s.name,
			Line:   line,
		})
	}
	if prefix {
		if line == "" {
			_, err := fmt.Fprintf(s.writer, "%s:\n", s.name)
			return err
		}
		_, err := fmt.Fprintf(s.writer, "%s: %s\n", s.name, line)
		return err
	}
	_, err := fmt.Fprintln(s.writer, line)
	return err
}

func readServiceLogFile(path string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return data, stat, nil
}

func tailLines(data []byte, count int) []string {
	if count <= 0 || len(data) == 0 {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if count >= len(lines) {
		return lines
	}
	return lines[len(lines)-count:]
}

func ServiceInstall() error {
	_, err := ServiceInstallWithReport()
	return err
}

func ServiceInstallWithReport() (ServiceEnvironmentReport, error) {
	if effectiveUID() != 0 {
		return ServiceEnvironmentReport{}, errors.New("service install must be run with sudo")
	}
	if err := unloadInstalledServiceDefinition(); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	report, err := syncLaunchdPlist()
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if _, err := startPreparedService(); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	return report, nil
}

func ServiceStart() error {
	_, err := ServiceStartWithReport()
	return err
}

func ServiceStartWithReport() (ServiceEnvironmentReport, error) {
	if effectiveUID() != 0 {
		return ServiceEnvironmentReport{}, errors.New("service start must be run with sudo")
	}
	if _, err := os.Stat(launchdPlistPath); err != nil {
		if os.IsNotExist(err) {
			return ServiceEnvironmentReport{}, errors.New("service is not installed (run `sudo switchd service install`)")
		}
		return ServiceEnvironmentReport{}, err
	}
	report, err := syncLaunchdPlist()
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	st, err := ServiceStatusInfo()
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if st.Running {
		return report, nil
	}
	if _, err := startPreparedService(); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	return report, nil
}

func ServiceStop() error {
	if effectiveUID() != 0 {
		return errors.New("service stop must be run with sudo")
	}
	st, err := ServiceStatusInfo()
	if err != nil {
		return err
	}
	if !st.Installed && !st.Running && !st.Stale {
		return nil
	}
	_, err = launchctlRun("bootout", launchdServiceTarget())
	if err != nil && !isLaunchctlNotLoaded(err) {
		return err
	}
	if !st.Running {
		_ = os.Remove(serviceStatePath)
		return nil
	}
	return waitForServiceStopped(serviceStopTimeout)
}

func ServiceUninstall() error {
	if effectiveUID() != 0 {
		return errors.New("service uninstall must be run with sudo")
	}
	if err := ServiceStop(); err != nil && !isLaunchctlNotLoaded(err) {
		return err
	}
	_, err := launchctlRun("disable", launchdServiceTarget())
	if err != nil && !isLaunchctlNotLoaded(err) && !isLaunchctlNoop(err) {
		return err
	}
	if err := os.Remove(launchdPlistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.RemoveAll(launchdRuntimeDir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ServiceStatusInfo() (LaunchdServiceStatus, error) {
	st := LaunchdServiceStatus{
		Label:            launchdServiceLabel,
		PlistPath:        launchdPlistPath,
		RuntimeStatePath: serviceStatePath,
		LogDir:           launchdLogDir,
	}
	report, reportErr := ServiceEnvironmentInfo()
	if reportErr == nil {
		st.ConfigPath = report.ConfigPath
		st.EnvFilePath = report.EnvFilePath
		st.RequiredEnvVars = report.RequiredEnvVars
		st.ConfiguredEnvVars = report.ConfiguredEnvVars
		st.MissingEnvVars = report.MissingEnvVars
	}
	if _, err := os.Stat(launchdPlistPath); err == nil {
		st.Installed = true
	} else if err != nil && !os.IsNotExist(err) {
		return st, err
	}
	state, err := readDaemonRuntimeState()
	if err != nil {
		if !os.IsNotExist(err) {
			st.Stale = true
			st.StateError = err.Error()
		}
		return st, nil
	}
	st.ConfigPath = state.ConfigPath
	st.PID = state.PID
	st.CaddyPID = state.CaddyPID
	st.StartedAt = state.StartedAt
	st.Ready = state.Ready
	st.Phase = state.Phase
	st.StateError = strings.TrimSpace(state.LastError)
	if state.PID > 0 && processAlive(state.PID) {
		st.Running = true
		return st, nil
	}
	st.Stale = true
	return st, nil
}

func DaemonRun() error {
	if effectiveUID() != 0 {
		return errors.New("daemon run must be run with sudo")
	}
	svc := DefaultService()
	cfgPath, err := svc.ConfigPath()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	d := &backgroundDaemon{
		service:    svc,
		configPath: cfgPath,
		now:        time.Now,
	}
	return d.Run(ctx)
}

func (d *backgroundDaemon) Run(ctx context.Context) error {
	if d == nil || d.service == nil {
		return fmt.Errorf("daemon service is nil")
	}
	lock, err := acquireDaemonLock(serviceLockPath)
	if err != nil {
		return err
	}
	defer lock.close()

	state := daemonRuntimeState{
		PID:        os.Getpid(),
		StartedAt:  d.now().UTC().Format(time.RFC3339),
		ConfigPath: d.configPath,
		Phase:      "starting",
	}
	if err := writeDaemonRuntimeState(state); err != nil {
		return err
	}
	fail := func(phase string, err error) error {
		state.Phase = phase
		state.Ready = false
		if err != nil {
			state.LastError = strings.TrimSpace(err.Error())
		}
		_ = writeDaemonRuntimeState(state)
		return err
	}

	cfgPath, cfg, err := d.service.LoadOrCreateDefaultConfig()
	if err != nil {
		return fail("config_error", err)
	}
	state.ConfigPath = cfgPath
	state.Phase = "config_loaded"
	if err := writeDaemonRuntimeState(state); err != nil {
		return err
	}

	state.Phase = "starting_caddy"
	if err := writeDaemonRuntimeState(state); err != nil {
		return err
	}
	caddyProc, err := startManagedCaddy(cfgPath)
	if err != nil {
		return fail("caddy_start_error", err)
	}
	state.CaddyPID = caddyProc.PID()
	state.Phase = "waiting_for_caddy"
	if err := writeDaemonRuntimeState(state); err != nil {
		return err
	}

	if err := waitForCaddyAdmin(cfg.Caddy.Admin, 15*time.Second); err != nil {
		_ = terminateManagedProcess(caddyProc, 2*time.Second)
		return fail("caddy_ready_error", err)
	}
	state.Phase = "applying_config"
	if err := writeDaemonRuntimeState(state); err != nil {
		return err
	}
	if err := d.service.ApplyConfig(cfgPath, cfg); err != nil {
		_ = terminateManagedProcess(caddyProc, 2*time.Second)
		return fail("apply_error", err)
	}
	state.Phase = "resuming_apps"
	if err := writeDaemonRuntimeState(state); err != nil {
		return err
	}
	resumeReport, err := d.resumePersistedApps()
	if err != nil {
		_ = terminateManagedProcess(caddyProc, 2*time.Second)
		return fail("resume_error", err)
	}
	state.Ready = true
	state.LastError = ""
	state.Phase = "ready"
	if len(resumeReport.FailedApps) > 0 {
		state.Phase = "ready_degraded"
		state.LastError = strings.Join(resumeReport.FailedApps, "; ")
	}
	if err := writeDaemonRuntimeState(state); err != nil {
		return err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- caddyProc.Wait()
	}()

	select {
	case <-ctx.Done():
		state.Ready = false
		state.Phase = "stopping"
		state.LastError = ""
		_ = writeDaemonRuntimeState(state)
		if err := d.shutdown(caddyProc); err != nil {
			return fail("shutdown_error", err)
		}
		_ = os.Remove(serviceStatePath)
		return nil
	case err := <-waitCh:
		state.Ready = false
		state.Phase = "caddy_exited"
		if err == nil {
			return fail("caddy_exited", errors.New("caddy exited unexpectedly"))
		}
		return fail("caddy_exited", fmt.Errorf("caddy exited: %w", err))
	}
}

func (d *backgroundDaemon) resumePersistedApps() (daemonResumeReport, error) {
	cfgPath, cfg, err := d.service.LoadOrCreateDefaultConfig()
	if err != nil {
		return daemonResumeReport{}, err
	}
	report := daemonResumeReport{}
	changed := false
	for _, a := range cfg.Apps {
		if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) == "" {
			continue
		}
		report.ActiveApps++
		if _, err := d.service.EnsureAppRuntime(cfg, a.Name); err != nil {
			report.FailedApps = append(report.FailedApps, fmt.Sprintf("%s: %v", a.Name, err))
			continue
		}
		report.ResumedApps++
		changed = true
	}
	if !changed {
		return report, nil
	}
	return report, d.service.SaveConfigAt(cfgPath, cfg)
}

func (d *backgroundDaemon) shutdown(caddyProc daemonProcess) error {
	cfgPath, cfg, err := d.service.LoadOrCreateDefaultConfig()
	if err != nil {
		return err
	}
	changed := false
	for _, a := range cfg.Apps {
		if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) == "" {
			continue
		}
		if _, err := d.service.StopAppRuntime(cfg, a.Name); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if err := d.service.SaveConfigAt(cfgPath, cfg); err != nil {
			return err
		}
	}
	return terminateManagedProcess(caddyProc, 5*time.Second)
}

func waitForServiceReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := ServiceStatusInfo()
		if err == nil && st.Running && st.Ready {
			return nil
		}
		if err == nil && st.Stale && strings.TrimSpace(st.StateError) != "" {
			return fmt.Errorf("background service failed to start: %s", st.StateError)
		}
		time.Sleep(200 * time.Millisecond)
	}
	st, err := ServiceStatusInfo()
	if err == nil && strings.TrimSpace(st.StateError) != "" {
		return fmt.Errorf("background service did not become ready: %s", st.StateError)
	}
	return errors.New("timed out waiting for background service to become ready")
}

func waitForServiceStopped(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := ServiceStatusInfo()
		if err == nil && !st.Running && !runtimeStateExists() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("timed out waiting for background service to stop")
}

func unloadInstalledServiceDefinition() error {
	_, err := launchctlRun("bootout", launchdServiceTarget())
	if err != nil && !isLaunchctlNotLoaded(err) {
		return err
	}
	_ = os.Remove(serviceStatePath)
	return nil
}

func runtimeStateExists() bool {
	_, err := os.Stat(serviceStatePath)
	return err == nil
}

func ServiceEnvironmentInfo() (ServiceEnvironmentReport, error) {
	configPath, err := cfgPath()
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	report, _, err := resolveServiceEnvironment(configPath)
	return report, err
}

func PrepareServiceEnvironment() (ServiceEnvironmentReport, error) {
	configPath, err := cfgPath()
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	report, _, err := resolveServiceEnvironment(configPath)
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if err := ensureServiceEnvTemplate(&report); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	return report, nil
}

func startPreparedService() (LaunchdServiceStatus, error) {
	if err := unloadInstalledServiceDefinition(); err != nil {
		return LaunchdServiceStatus{}, err
	}
	_, err := launchctlRun("enable", launchdServiceTarget())
	if err != nil && !isLaunchctlNoop(err) {
		return LaunchdServiceStatus{}, err
	}
	if err := prepareServiceLogFiles(); err != nil {
		return LaunchdServiceStatus{}, err
	}
	_, err = launchctlRun("bootstrap", launchdServiceDomain, launchdPlistPath)
	if err != nil && !isLaunchctlAlreadyLoaded(err) {
		return LaunchdServiceStatus{}, err
	}
	if _, err := launchctlRun("kickstart", "-kp", launchdServiceTarget()); err != nil {
		return LaunchdServiceStatus{}, err
	}
	if err := waitForServiceReady(serviceReadyTimeout); err != nil {
		return LaunchdServiceStatus{}, err
	}
	return ServiceStatusInfo()
}

func syncLaunchdPlist() (ServiceEnvironmentReport, error) {
	configPath, err := cfgPath()
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	report, env, err := resolveServiceEnvironment(configPath)
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if err := ensureServiceEnvTemplate(&report); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	exe, err := resolveExecutable()
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if err := os.MkdirAll(filepath.Dir(launchdPlistPath), 0o755); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if err := os.MkdirAll(launchdRuntimeDir, 0o755); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		return ServiceEnvironmentReport{}, err
	}
	plist, err := renderLaunchdPlist(launchdPlistSpec{
		Label:            launchdServiceLabel,
		ProgramArguments: []string{exe, "daemon", "run"},
		Environment:      env,
		StandardOutPath:  serviceStdoutPath,
		StandardErrPath:  serviceStderrPath,
	})
	if err != nil {
		return ServiceEnvironmentReport{}, err
	}
	if err := os.WriteFile(launchdPlistPath, plist, 0o644); err != nil {
		return ServiceEnvironmentReport{}, fmt.Errorf("write launchd plist: %w", err)
	}
	return report, nil
}

func resolveServiceEnvironment(configPath string) (ServiceEnvironmentReport, map[string]string, error) {
	cfg, err := config.LoadOrDefault(configPath)
	if err != nil {
		return ServiceEnvironmentReport{}, nil, err
	}
	report := ServiceEnvironmentReport{
		ConfigPath:  configPath,
		EnvFilePath: serviceEnvPath(configPath),
	}
	required := requiredServiceEnvVars(cfg)
	report.RequiredEnvVars = append(report.RequiredEnvVars, required...)
	fileVars, err := loadServiceEnvFile(report.EnvFilePath)
	if err != nil {
		return ServiceEnvironmentReport{}, nil, err
	}
	uid, gid := configOwnerIDs()
	env := map[string]string{
		"PATH":                     "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME":                     defaultServiceHome(),
		"SWITCHD_CONFIG_PATH":      configPath,
		"SWITCHD_CONFIG_OWNER_UID": uid,
		"SWITCHD_CONFIG_OWNER_GID": gid,
	}
	for _, name := range required {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			value = strings.TrimSpace(fileVars[name])
		}
		if value == "" {
			report.MissingEnvVars = append(report.MissingEnvVars, name)
			continue
		}
		env[name] = value
		report.ConfiguredEnvVars = append(report.ConfiguredEnvVars, name)
	}
	return report, env, nil
}

func requiredServiceEnvVars(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
	}
	for providerName, providerCfg := range cfg.Tunnel.Providers {
		for key, value := range providerCfg.Values {
			if strings.HasSuffix(strings.ToLower(strings.TrimSpace(key)), "_env") {
				add(value)
			}
		}
		if strings.EqualFold(strings.TrimSpace(providerName), "cloudflare") {
			mode := strings.ToLower(strings.TrimSpace(providerCfg.Values["mode"]))
			if mode == "api" {
				envName := strings.TrimSpace(providerCfg.Values["api_token_env"])
				if envName == "" {
					envName = "SWITCHD_CF_API_TOKEN"
				}
				add(envName)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func serviceEnvPath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "service.env")
}

func loadServiceEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ensureServiceEnvTemplate(report *ServiceEnvironmentReport) error {
	if report == nil || len(report.MissingEnvVars) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(report.EnvFilePath), 0o755); err != nil {
		return err
	}

	existing, err := os.ReadFile(report.EnvFilePath)
	created := false
	switch {
	case err == nil:
	case os.IsNotExist(err):
		created = true
	default:
		return err
	}
	existingVars, err := loadServiceEnvFile(report.EnvFilePath)
	if err != nil {
		return err
	}

	var out strings.Builder
	if len(existing) > 0 {
		out.Write(existing)
		if existing[len(existing)-1] != '\n' {
			out.WriteByte('\n')
		}
	} else {
		out.WriteString("# switchd launchd environment\n")
	}
	for _, name := range report.MissingEnvVars {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := existingVars[name]; ok {
			continue
		}
		out.WriteString(name)
		out.WriteString("=\n")
		report.EnvFileTemplateVars = append(report.EnvFileTemplateVars, name)
	}
	if len(report.EnvFileTemplateVars) == 0 {
		return nil
	}
	if err := os.WriteFile(report.EnvFilePath, []byte(out.String()), 0o600); err != nil {
		return err
	}
	fixSudoOwnership(report.EnvFilePath)
	report.EnvFileCreated = created
	report.EnvFileUpdated = true
	return nil
}

func defaultServiceHome() string {
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return home
	}
	return "/var/root"
}

func prepareServiceLogFiles() error {
	if err := os.MkdirAll(launchdLogDir, 0o755); err != nil {
		return err
	}
	for _, path := range []string{serviceStdoutPath, serviceStderrPath} {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

func readDaemonRuntimeState() (daemonRuntimeState, error) {
	b, err := os.ReadFile(serviceStatePath)
	if err != nil {
		return daemonRuntimeState{}, err
	}
	var st daemonRuntimeState
	if err := json.Unmarshal(b, &st); err != nil {
		return daemonRuntimeState{}, fmt.Errorf("decode runtime state: %w", err)
	}
	return st, nil
}

func writeDaemonRuntimeState(st daemonRuntimeState) error {
	if err := os.MkdirAll(launchdRuntimeDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(serviceStatePath, b, 0o644)
}

func acquireDaemonLock(path string) (*daemonLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("switchd background daemon is already running")
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.Seek(0, 0)
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return &daemonLock{file: f}, nil
}

func (l *daemonLock) close() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	name := l.file.Name()
	_ = l.file.Close()
	_ = os.Remove(name)
}

func startManagedCaddy(configPath string) (daemonProcess, error) {
	bootstrap, _, err := ensureBootstrapCaddyfile(configPath)
	if err != nil {
		return nil, err
	}
	return startBackgroundCmd("caddy", "run", "--config", bootstrap, "--adapter", "caddyfile")
}

func terminateManagedProcess(proc daemonProcess, timeout time.Duration) error {
	if proc == nil || proc.PID() <= 0 {
		return nil
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- proc.Wait()
	}()
	if err := proc.Signal(syscall.SIGTERM); err != nil && !isProcessGone(err) {
		return err
	}
	select {
	case err := <-waitCh:
		if err != nil && !isProcessGone(err) {
			return err
		}
		return nil
	case <-time.After(timeout):
		if err := proc.Kill(); err != nil && !isProcessGone(err) {
			return err
		}
		<-waitCh
		return nil
	}
}

func isProcessGone(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "process already finished") ||
		strings.Contains(msg, "no such process")
}

func defaultStartBackgroundCommand(name string, args ...string) (daemonProcess, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = backgroundCommandEnv()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execBackgroundProcess{cmd: cmd}, nil
}

func backgroundCommandEnv() []string {
	env := append([]string{}, os.Environ()...)
	return ensureEnvValue(env, "HOME", defaultServiceHome())
}

func ensureEnvValue(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			continue
		}
		if strings.TrimSpace(entry[len(prefix):]) == "" {
			env[i] = prefix + value
		}
		return env
	}
	return append(env, prefix+value)
}

func defaultWaitForCaddyAdmin(adminBase string, timeout time.Duration) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	url := strings.TrimRight(strings.TrimSpace(adminBase), "/") + "/config/"
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for caddy admin at %s", adminBase)
}

func defaultLaunchctlRun(args ...string) (string, error) {
	cmd := exec.Command("launchctl", args...)
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return msg, fmt.Errorf("launchctl %s: %s", strings.Join(args, " "), msg)
	}
	return msg, nil
}

func defaultResolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe = filepath.Clean(exe)
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && strings.TrimSpace(resolved) != "" {
		exe = resolved
	}
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("switchd executable path must be absolute: %q", exe)
	}
	return exe, nil
}

func configOwnerIDs() (string, string) {
	uid := strings.TrimSpace(os.Getenv("SUDO_UID"))
	gid := strings.TrimSpace(os.Getenv("SUDO_GID"))
	if uid != "" && gid != "" {
		return uid, gid
	}
	uid = strings.TrimSpace(os.Getenv("SWITCHD_CONFIG_OWNER_UID"))
	gid = strings.TrimSpace(os.Getenv("SWITCHD_CONFIG_OWNER_GID"))
	if uid != "" && gid != "" {
		return uid, gid
	}
	return strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
}

func isLaunchctlAlreadyLoaded(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already bootstrapped") ||
		strings.Contains(msg, "already loaded") ||
		strings.Contains(msg, "service already exists")
}

func isLaunchctlNotLoaded(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not find service") ||
		strings.Contains(msg, "service is disabled") ||
		strings.Contains(msg, "no such process") ||
		strings.Contains(msg, "not loaded")
}

func isLaunchctlNoop(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already enabled") ||
		strings.Contains(msg, "already disabled")
}

type launchdPlistSpec struct {
	Label            string
	ProgramArguments []string
	Environment      map[string]string
	StandardOutPath  string
	StandardErrPath  string
}

func renderLaunchdPlist(spec launchdPlistSpec) ([]byte, error) {
	if strings.TrimSpace(spec.Label) == "" {
		return nil, fmt.Errorf("launchd label is required")
	}
	if len(spec.ProgramArguments) == 0 {
		return nil, fmt.Errorf("launchd program arguments are required")
	}
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	buf.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	buf.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	plistKeyValue(&buf, "Label", spec.Label)
	buf.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, arg := range spec.ProgramArguments {
		buf.WriteString("    <string>")
		xmlEscape(&buf, arg)
		buf.WriteString("</string>\n")
	}
	buf.WriteString("  </array>\n")
	buf.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	buf.WriteString("  <key>KeepAlive</key>\n  <true/>\n")
	if len(spec.Environment) > 0 {
		keys := make([]string, 0, len(spec.Environment))
		for k := range spec.Environment {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, k := range keys {
			plistKeyValue(&buf, k, spec.Environment[k])
		}
		buf.WriteString("  </dict>\n")
	}
	if strings.TrimSpace(spec.StandardOutPath) != "" {
		plistKeyValue(&buf, "StandardOutPath", spec.StandardOutPath)
	}
	if strings.TrimSpace(spec.StandardErrPath) != "" {
		plistKeyValue(&buf, "StandardErrorPath", spec.StandardErrPath)
	}
	buf.WriteString("</dict>\n</plist>\n")
	return buf.Bytes(), nil
}

func plistKeyValue(buf *bytes.Buffer, key, value string) {
	buf.WriteString("  <key>")
	xmlEscape(buf, key)
	buf.WriteString("</key>\n  <string>")
	xmlEscape(buf, value)
	buf.WriteString("</string>\n")
}

func xmlEscape(buf *bytes.Buffer, value string) {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	buf.WriteString(replacer.Replace(value))
}
