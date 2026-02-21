package dns

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/sys"
)

const (
	managedBegin = "# BEGIN switchboard-hub (managed by switchd)"
	managedEnd   = "# END switchboard-hub (managed by switchd)"

	launchdPlistPath = "/Library/LaunchDaemons/com.switchboard-hub.lo0-alias.plist"
	launchdLabel     = "com.switchboard-hub.lo0-alias"
	resolverDir      = "/etc/resolver"
)

func Apply(c *config.Config) error {
	if err := sys.Run("/sbin/ifconfig", "lo0", "alias", c.DNS.IP); err != nil {
		return fmt.Errorf("failed to add loopback alias %s: %w", c.DNS.IP, err)
	}
	fmt.Println("added loopback alias:", c.DNS.IP)

	if err := installLaunchDaemon(c.DNS.IP); err != nil {
		return err
	}

	if err := writeResolver(c.TLD, c.DNS.IP); err != nil {
		return err
	}

	brew, err := sys.FindBrew()
	if err != nil {
		return err
	}
	prefix, err := sys.RunCapture(brew, "--prefix")
	if err != nil {
		return fmt.Errorf("brew --prefix failed: %w", err)
	}
	prefix = strings.TrimSpace(prefix)
	dnsmasqConf := filepath.Join(prefix, "etc", "dnsmasq.conf")

	block := buildManagedBlock(c.TLD, c.DNS.IP)

	if err := upsertManagedBlock(dnsmasqConf, block); err != nil {
		return err
	}
	fmt.Println("updated:", dnsmasqConf)

	fmt.Println()
	fmt.Println("Restart dnsmasq (required):")
	fmt.Println("  sudo", brew, "services restart dnsmasq")
	return nil
}

func Uninstall(c *config.Config) error {
	brew, _ := sys.FindBrew()
	prefix := ""
	if brew != "" {
		if out, err := sys.RunCapture(brew, "--prefix"); err == nil {
			prefix = strings.TrimSpace(out)
		}
	}

	if prefix != "" {
		dnsmasqConf := filepath.Join(prefix, "etc", "dnsmasq.conf")
		if err := removeManagedBlock(dnsmasqConf); err != nil {
			return err
		}
		fmt.Println("updated:", dnsmasqConf)
	} else {
		fmt.Println("brew not found; skipping dnsmasq.conf update")
	}

	resolverPath := filepath.Join(resolverDir, c.TLD)
	_ = os.Remove(resolverPath)
	fmt.Println("removed:", resolverPath)

	_ = sys.Run("launchctl", "bootout", "system", launchdPlistPath)
	_ = sys.Run("launchctl", "disable", "system/"+launchdLabel)
	_ = os.Remove(launchdPlistPath)
	fmt.Println("removed:", launchdPlistPath)

	_ = sys.Run("/sbin/ifconfig", "lo0", "-alias", c.DNS.IP)
	fmt.Println("removed loopback alias:", c.DNS.IP)

	if brew != "" {
		fmt.Println()
		fmt.Println("Restart dnsmasq:")
		fmt.Println("  sudo", brew, "services restart dnsmasq")
	}
	return nil
}

func buildManagedBlock(tld, ip string) string {
	return strings.Join([]string{
		managedBegin,
		"port=53",
		fmt.Sprintf("listen-address=%s", ip),
		"bind-interfaces",
		fmt.Sprintf("address=/.%s/127.0.0.1", tld),
		managedEnd,
		"",
	}, "\n")
}

func upsertManagedBlock(path string, block string) error {
	orig, _ := os.ReadFile(path)
	s := string(orig)

	if strings.Contains(s, managedBegin) && strings.Contains(s, managedEnd) {
		start := strings.Index(s, managedBegin)
		end := strings.Index(s, managedEnd)
		if start < 0 || end < 0 || end < start {
			return fmt.Errorf("malformed managed block in %s", path)
		}
		end = end + len(managedEnd)
		newS := s[:start] + strings.TrimRight(block, "\n") + s[end:]
		return atomicWrite(path, []byte(newS), 0o644)
	}

	var buf bytes.Buffer
	buf.Write(orig)
	if len(orig) > 0 && !strings.HasSuffix(s, "\n") {
		buf.WriteString("\n")
	}
	buf.WriteString(block)
	return atomicWrite(path, buf.Bytes(), 0o644)
}

func removeManagedBlock(path string) error {
	orig, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := string(orig)
	if !strings.Contains(s, managedBegin) || !strings.Contains(s, managedEnd) {
		return nil
	}
	start := strings.Index(s, managedBegin)
	end := strings.Index(s, managedEnd)
	if start < 0 || end < 0 || end < start {
		return fmt.Errorf("malformed managed block in %s", path)
	}
	end = end + len(managedEnd)

	newS := s[:start] + s[end:]
	newS = strings.ReplaceAll(newS, "\n\n\n", "\n\n")
	return atomicWrite(path, []byte(newS), 0o644)
}

func writeResolver(tld, ip string) error {
	if err := os.MkdirAll(resolverDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(resolverDir, tld)
	content := fmt.Sprintf("nameserver %s\n", ip)
	if err := atomicWrite(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote:", path)
	return nil
}

func installLaunchDaemon(ip string) error {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>%s</string>

    <key>ProgramArguments</key>
    <array>
      <string>/sbin/ifconfig</string>
      <string>lo0</string>
      <string>alias</string>
      <string>%s</string>
    </array>

    <key>RunAtLoad</key>
    <true/>
  </dict>
</plist>
`, launchdLabel, ip)

	if err := atomicWrite(launchdPlistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote:", launchdPlistPath)

	_ = sys.Run("launchctl", "bootstrap", "system", launchdPlistPath)
	_ = sys.Run("launchctl", "enable", "system/"+launchdLabel)
	_ = sys.Run("launchctl", "kickstart", "-k", "system/"+launchdLabel)
	return nil
}

func atomicWrite(path string, content []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
