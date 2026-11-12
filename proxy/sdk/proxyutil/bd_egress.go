package proxyutil

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
)

// Bright Data ISP static-IP helpers.
// The zone password is resolved from the process environment (a mounted K8s
// Secret) at request-build time and must never be written to auth_json,
// Postgres credential rows, logs, or panel payloads.

const (
	// BDPasswordEnv is the environment variable holding the Bright Data zone password.
	BDPasswordEnv = "CLIPROXY_BD_ISP_PASSWORD"
	// BDPasswordFileEnv, when set, is a file whose first line is the zone password
	// (typical Secret mount). It takes precedence over BDPasswordEnv when the file
	// can be read.
	BDPasswordFileEnv = "CLIPROXY_BD_ISP_PASSWORD_FILE"
	// EgressKindBD is the credential/egress kind for Bright Data static IPs.
	EgressKindBD = "bd"
	// EgressKindKey is the auth metadata key storing the egress kind.
	EgressKindKey = "egress_kind"
	// EgressIDKey is the auth metadata key storing the stable egress id.
	EgressIDKey = "egress_id"
	// DefaultBDUsernameTemplate matches kiro's ISP static selector.
	DefaultBDUsernameTemplate = "{baseUsername}-ip-{ip}-dns-remote-route_err-block"
	// DefaultBDEndpoint is Bright Data's ISP super-proxy.
	DefaultBDEndpoint = "brd.superproxy.io:44445"
	// DefaultBDRelayPort is CPA's own Squid relay port (not kiro's 3129).
	DefaultBDRelayPort = 3142
	// BDIDPrefix is the stable id prefix: "bd/<static-ip>".
	BDIDPrefix = "bd/"
)

// RenderBDUsername builds the Bright Data zone username that pins a CONNECT to ip.
// template may be empty (the default is used). A completely custom username for
// one IP can be passed as template with no placeholders (used as-is).
func RenderBDUsername(baseUsername, template, ip string) string {
	baseUsername = strings.TrimSpace(baseUsername)
	ip = strings.TrimSpace(ip)
	template = strings.TrimSpace(template)
	if template == "" {
		template = DefaultBDUsernameTemplate
	}
	if !strings.Contains(template, "{") {
		return template
	}
	out := strings.ReplaceAll(template, "{baseUsername}", baseUsername)
	out = strings.ReplaceAll(out, "{ip}", ip)
	return out
}

// BDEgressID returns the stable id "bd/<static-ip>".
func BDEgressID(ip string) string {
	ip = strings.TrimSpace(ip)
	if strings.HasPrefix(ip, BDIDPrefix) {
		return ip
	}
	return BDIDPrefix + ip
}

// IsBDEgressID reports whether id is a Bright Data egress id.
func IsBDEgressID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), BDIDPrefix)
}

// IsBDAuth reports whether an auth is pinned to a Bright Data egress, based on
// metadata only (kind or id). The proxy URL is not consulted so a leaked
// password in a URL cannot change the classification.
func IsBDAuth(metadata map[string]any, egressID string) bool {
	if metadata != nil {
		if k, _ := metadata[EgressKindKey].(string); strings.EqualFold(strings.TrimSpace(k), EgressKindBD) {
			return true
		}
		if id, _ := metadata[EgressIDKey].(string); IsBDEgressID(id) {
			return true
		}
	}
	return IsBDEgressID(egressID)
}

// StripProxyPassword returns u with any user password removed. The username
// (BD selector) is kept. Invalid URLs are returned unchanged (they have no
// usable password).
func StripProxyPassword(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, has := u.User.Password(); !has {
		return raw
	}
	u.User = url.User(u.User.Username())
	return u.String()
}

// RedactProxyURL replaces a proxy URL's password with "***" so the result is
// safe to log or return to a client. It also redacts user:password@ patterns
// that appear inside an error string.
func RedactProxyURL(raw string) string {
	if raw == "" {
		return raw
	}
	// Full URL.
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "***")
			raw = u.String()
		}
	}
	// Embedded user:pass@ in an error message (url.Error often includes the URL).
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if at := strings.Index(rest, "@"); at > 0 {
			cred := rest[:at]
			if colon := strings.Index(cred, ":"); colon >= 0 {
				redacted := raw[:i+3] + cred[:colon] + ":***@" + rest[at+1:]
				return redacted
			}
		}
	}
	return raw
}

// InjectBDPassword returns a copy of the password-less relay URL with password
// set from the Secret-sourced value. An existing password in raw (which should
// never be stored) is discarded and replaced, so a leaked stored value cannot
// override the Secret. The password is not logged.
func InjectBDPassword(raw, password string) (string, error) {
	raw = strings.TrimSpace(raw)
	password = strings.TrimSpace(password)
	if raw == "" {
		return "", fmt.Errorf("bd proxy url is empty")
	}
	if password == "" {
		return "", fmt.Errorf("bd zone password is not configured")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("bd proxy url: %s", RedactProxyURL(err.Error()))
	}
	if u.Host == "" {
		return "", fmt.Errorf("bd proxy url has no host")
	}
	user := ""
	if u.User != nil {
		user = u.User.Username()
	}
	if user == "" {
		return "", fmt.Errorf("bd proxy url has no username")
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}

// EffectiveProxyURL returns the URL to use when building an HTTP client for
// auth. For BD auths the Secret password is injected; for others the stored
// URL is returned unchanged (already complete). The result for BD must not be
// persisted or logged.
func EffectiveProxyURL(storedURL string, metadata map[string]any, password string) (string, error) {
	storedURL = strings.TrimSpace(storedURL)
	if !IsBDAuth(metadata, "") {
		return storedURL, nil
	}
	// Always start from a password-less URL so a leftover stored password is ignored.
	return InjectBDPassword(StripProxyPassword(storedURL), password)
}

// BuildBDRelayURL builds the password-less proxy URL a credential stores:
// http://{username}@{relayHost} with no password.
func BuildBDRelayURL(username, relayHost string) (string, error) {
	username = strings.TrimSpace(username)
	relayHost = strings.TrimSpace(relayHost)
	if username == "" {
		return "", fmt.Errorf("bd username is empty")
	}
	if relayHost == "" {
		return "", fmt.Errorf("bd relay host is empty")
	}
	if _, _, err := net.SplitHostPort(relayHost); err != nil {
		// Allow a host without a port; the caller should have included one.
		if !strings.Contains(relayHost, ":") {
			relayHost = net.JoinHostPort(relayHost, fmt.Sprintf("%d", DefaultBDRelayPort))
		} else {
			return "", fmt.Errorf("bd relay host: %v", err)
		}
	}
	u := &url.URL{Scheme: "http", Host: relayHost, User: url.User(username)}
	return u.String(), nil
}

var (
	bdPasswordMu       sync.Mutex
	bdPasswordOverride *string // tests only
)

// LoadBDPassword reads the Bright Data zone password from the Secret-backed
// environment. Empty means "not configured". This value must never be logged.
func LoadBDPassword() string {
	bdPasswordMu.Lock()
	override := bdPasswordOverride
	bdPasswordMu.Unlock()
	if override != nil {
		return *override
	}
	if path := strings.TrimSpace(os.Getenv(BDPasswordFileEnv)); path != "" {
		raw, err := os.ReadFile(path)
		if err == nil {
			// First line only; ignore trailing whitespace.
			line, _, _ := strings.Cut(string(raw), "\n")
			if p := strings.TrimSpace(line); p != "" {
				return p
			}
		}
	}
	return strings.TrimSpace(os.Getenv(BDPasswordEnv))
}

// HasBDPassword reports whether a zone password is available, without exposing it.
func HasBDPassword() bool {
	return LoadBDPassword() != ""
}

// SetBDPasswordForTest overrides LoadBDPassword in tests. Pass nil to clear.
func SetBDPasswordForTest(password *string) {
	bdPasswordMu.Lock()
	bdPasswordOverride = password
	bdPasswordMu.Unlock()
}
