package target

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackprince001/flowbench/internal/ir"
)

// Target is a validated target config ready to enforce: it knows the base URL
// relative calls resolve against and the set of hosts they may reach.
type Target struct {
	cfg     *ir.TargetConfig
	base    string
	origins map[string]bool
}

// New builds a Target from a config. The config's base URLs are the host
// allow-list; the first is the default base for relative call URLs.
func New(cfg *ir.TargetConfig) (*Target, error) {
	if len(cfg.BaseURLs) == 0 {
		return nil, fmt.Errorf("target %q declares no base URLs", cfg.Name)
	}
	origins := make(map[string]bool, len(cfg.BaseURLs))
	for _, raw := range cfg.BaseURLs {
		o, err := originOf(raw)
		if err != nil {
			return nil, fmt.Errorf("target %q base URL %q: %w", cfg.Name, raw, err)
		}
		origins[o] = true
	}
	return &Target{
		cfg:     cfg,
		base:    strings.TrimRight(cfg.BaseURLs[0], "/"),
		origins: origins,
	}, nil
}

func (t *Target) Config() *ir.TargetConfig { return t.cfg }

// BaseURL is prepended to relative call URLs.
func (t *Target) BaseURL() string { return t.base }

// RequestTimeout bounds one call against this target; zero leaves the adapter's
// default in place.
func (t *Target) RequestTimeout() time.Duration { return time.Duration(t.cfg.RequestTimeout) }

// Allows reports whether a fully resolved call URL targets an allowed host.
// Relative URLs are allowed because they resolve against the base URL. This is
// the request-time check for URLs whose host is only known after templating.
func (t *Target) Allows(rawURL string) (bool, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, fmt.Errorf("parse url %q: %w", rawURL, err)
	}
	if u.Scheme == "" && u.Host == "" {
		return true, nil
	}
	return t.origins[strings.ToLower(u.Scheme)+"://"+strings.ToLower(u.Host)], nil
}

// Check is the pre-run gate: it refuses a scenario whose statically-knowable
// call hosts fall outside the allow-list, or whose mode the target disallows,
// before any request is sent. Call URLs whose host is templated are only known
// at request time and are enforced there via Allows.
func (t *Target) Check(sc *ir.Scenario) error {
	var problems []string
	for _, f := range sc.Flows {
		for i := range f.Steps {
			st := &f.Steps[i]
			for _, raw := range stepURLs(st) {
				if !hasScheme(raw) {
					continue // relative → resolves against the base URL
				}
				auth := authority(raw)
				if strings.Contains(auth, "{{") {
					continue // dynamic host, enforced at request time
				}
				origin := strings.ToLower(schemeOf(raw)) + "://" + strings.ToLower(auth)
				if !t.origins[origin] {
					problems = append(problems, fmt.Sprintf("flow %q step %q calls %s", f.Name, st.ID, raw))
				}
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("target %q allows only %v; refused: %s",
			t.cfg.Name, t.cfg.BaseURLs, strings.Join(problems, "; "))
	}
	if t.disallows(sc.Profile.Mode) {
		return fmt.Errorf("target %q disallows %q mode", t.cfg.Name, sc.Profile.Mode)
	}
	return nil
}

func (t *Target) disallows(mode ir.Mode) bool {
	for _, m := range t.cfg.DisallowedModes {
		if m == mode {
			return true
		}
	}
	return false
}

// Resolve maps a --target name to its config file: <dir>/<name>.yaml.
func Resolve(name, dir string) string {
	return filepath.Join(dir, name+".yaml")
}

// stepURLs is every host a step can reach — its call, plus an OAuth2 token
// endpoint, which is a real outbound request carrying env-sourced client
// credentials and so belongs under the same allow-list as the call itself.
func stepURLs(st *ir.Step) []string {
	var urls []string
	switch {
	case st.Call != nil:
		urls = append(urls, st.Call.URL)
	case st.Poll != nil:
		urls = append(urls, st.Poll.Call.URL)
	}
	if st.Auth != nil && st.Auth.TokenURL != "" {
		urls = append(urls, st.Auth.TokenURL)
	}
	return urls
}

func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%q is not an absolute URL", raw)
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// hasScheme reports whether raw begins with "scheme://" (an absolute URL),
// tolerating templates elsewhere that would trip url.Parse.
func hasScheme(raw string) bool {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return false
	}
	for _, r := range raw[:i] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '+', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

func schemeOf(raw string) string {
	i := strings.Index(raw, "://")
	return raw[:i]
}

// authority returns the host[:port] between "://" and the first "/", "?", or
// "#", by string slicing so a templated path does not confuse url.Parse.
func authority(raw string) string {
	rest := raw[strings.Index(raw, "://")+3:]
	end := len(rest)
	for _, c := range []byte{'/', '?', '#'} {
		if j := strings.IndexByte(rest, c); j >= 0 && j < end {
			end = j
		}
	}
	return rest[:end]
}
