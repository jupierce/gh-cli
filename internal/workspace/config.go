// Package workspace implements the credential-free Chai manager protocol.
// The descriptor is routing information, never an authorization grant.
package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Protocol identifies the manager contract supported by this patch series.
const Protocol = "chai.github-proxy/v1"

// Config contains public connection and Git identity information supplied by the manager.
type Config struct {
	Protocol         string   `json:"protocol"`
	Socket           string   `json:"socket"`
	Host             string   `json:"host"`
	GitProxyPrefixes []string `json:"git_proxy_prefixes"`
}

// Load reads an explicitly selected descriptor. Nil means ordinary upstream behavior.
func Load() (*Config, error) {
	filename, enabled := os.LookupEnv("GH_WORKSPACE_CONFIG")
	if !enabled {
		return nil, nil
	}
	if !filepath.IsAbs(filename) {
		return nil, fmt.Errorf("GH_WORKSPACE_CONFIG must name an absolute descriptor path")
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("read workspace descriptor: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read workspace descriptor: %w", err)
	}
	if len(data) > 64*1024 {
		return nil, fmt.Errorf("workspace descriptor exceeds 64 KiB")
	}
	var cfg Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode workspace descriptor: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("workspace descriptor must contain one JSON object")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate rejects ambiguous routes and incompatible descriptor versions.
func (c *Config) Validate() error {
	if c.Protocol != Protocol {
		return fmt.Errorf("unsupported workspace protocol %q (want %s)", c.Protocol, Protocol)
	}
	if !filepath.IsAbs(c.Socket) {
		return fmt.Errorf("workspace socket must be an absolute path")
	}
	if c.Host != "github.com" {
		return fmt.Errorf("workspace mode currently supports only github.com")
	}
	for _, prefix := range c.GitProxyPrefixes {
		u, err := url.Parse(prefix)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasSuffix(u.Path, "/") || path.Clean(u.Path)+"/" != u.Path {
			return fmt.Errorf("invalid workspace Git proxy prefix %q: use a canonical HTTP(S) URL ending in /", prefix)
		}
	}
	return nil
}

// Translator composes proxy identity translation with upstream SSH alias translation.
// It returns a copy and never changes the URLs Git uses for network operations.
type Translator struct {
	Config   *Config
	Fallback interface{ Translate(*url.URL) *url.URL }
}

// Translate recognizes only declared proxy prefixes, on path segment boundaries.
func (t Translator) Translate(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	if u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" {
		best := ""
		for _, prefix := range t.Config.GitProxyPrefixes {
			p, _ := url.Parse(prefix) // Config was validated at load time.
			if u.Scheme == p.Scheme && u.Host == p.Host && strings.HasPrefix(u.Path, p.Path) && len(p.Path) > len(best) {
				best = p.Path
			}
		}
		if best != "" {
			repo := strings.TrimPrefix(u.Path, best)
			parts := strings.Split(repo, "/")
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" && path.Clean(repo) == repo && parts[0] != "." && parts[1] != "." && parts[0] != ".." && parts[1] != ".." {
				return &url.URL{Scheme: "https", Host: t.Config.Host, Path: "/" + repo}
			}
		}
	}
	if t.Fallback != nil {
		return t.Fallback.Translate(u)
	}
	return u
}
