package diag

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
)

var debugEnabled atomic.Bool

func SetDebug(v bool) {
	debugEnabled.Store(v)
}

func Enabled() bool {
	return debugEnabled.Load()
}

func Debugf(format string, args ...any) {
	if !Enabled() {
		return
	}
	fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
}

func LogCommand(name string, args ...string) {
	if !Enabled() {
		return
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, sanitizeText(name))
	for _, a := range args {
		parts = append(parts, sanitizeArg(a))
	}
	Debugf("exec: %s", strings.Join(parts, " "))
}

func SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", sanitizeText(err.Error()))
}

func Redact(in string) string {
	return sanitizeText(in)
}

var (
	kvSecretRe = regexp.MustCompile(`(?i)(token|secret|password|api[_-]?key)\s*=\s*[^,\s]+`)
	bearerRe   = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-\._~\+\/=]+`)
)

func sanitizeArg(arg string) string {
	if strings.TrimSpace(arg) == "" {
		return arg
	}
	if strings.Contains(arg, "=") {
		parts := strings.SplitN(arg, "=", 2)
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "key") {
			return parts[0] + "=<redacted>"
		}
	}
	return sanitizeText(arg)
}

func sanitizeText(in string) string {
	out := kvSecretRe.ReplaceAllString(in, "$1=<redacted>")
	out = bearerRe.ReplaceAllString(out, "Bearer <redacted>")
	return out
}
