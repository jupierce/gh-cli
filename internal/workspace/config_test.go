package workspace

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "workspace.json")
	t.Setenv("GH_WORKSPACE_CONFIG", filename)
	cfg := Config{Protocol: Protocol, Host: "github.com", Socket: filepath.Join(dir, "api.sock"), GitProxyPrefixes: []string{"http://localhost:8080/github.com/"}}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filename, data, 0600))
	actual, err := Load()
	require.NoError(t, err)
	assert.Equal(t, &cfg, actual)

	for _, contents := range []string{`{`, `{"protocol":"other"}`, string(data) + ` {}`, string(data[:len(data)-1]) + `,"token":"should-not-be-here"}`} {
		require.NoError(t, os.WriteFile(filename, []byte(contents), 0600))
		_, err := Load()
		require.Error(t, err)
	}
	require.NoError(t, os.Remove(filename))
	_, err = Load()
	require.ErrorContains(t, err, "read workspace descriptor")
	t.Setenv("GH_WORKSPACE_CONFIG", "")
	_, err = Load()
	require.ErrorContains(t, err, "absolute descriptor path")
}

func TestValidate(t *testing.T) {
	for _, prefix := range []string{"http://localhost:8080/github.com", "http://user@localhost/github.com/", "http://localhost/a/../github.com/", "http://localhost/github.com/?x=y", "http://localhost/github%2ecom/", "ftp://localhost/github.com/"} {
		cfg := Config{Protocol: Protocol, Host: "github.com", Socket: "/run/gh.sock", GitProxyPrefixes: []string{prefix}}
		require.Error(t, cfg.Validate(), prefix)
	}
}

type sshFallback struct{}

func (sshFallback) Translate(u *url.URL) *url.URL {
	if u.Scheme == "ssh" && u.Host == "github-alias" {
		v := *u
		v.Host = "github.com"
		return &v
	}
	return u
}

func TestTranslate(t *testing.T) {
	translator := Translator{Config: &Config{Host: "github.com", GitProxyPrefixes: []string{"http://localhost:8080/github.com/", "http://localhost:8080/scoped/github.com/"}}, Fallback: sshFallback{}}
	for _, tt := range []struct{ input, want string }{
		{"http://localhost:8080/github.com/acme/project.git", "https://github.com/acme/project.git"},
		{"http://localhost:8080/scoped/github.com/acme/project.git", "https://github.com/acme/project.git"},
		{"ssh://github-alias/acme/project.git", "ssh://github.com/acme/project.git"},
		{"https://github.com/acme/project.git", "https://github.com/acme/project.git"},
		{"http://localhost:8081/github.com/acme/project.git", ""},
		{"http://localhost:8080/github.com.evil/acme/project.git", ""},
		{"http://localhost:8080/github.com/acme/project.git/extra", ""},
		{"http://localhost:8080/github.com/acme/../project.git", ""},
		{"http://user@localhost:8080/github.com/acme/project.git", ""},
		{"http://localhost:8080/github.com/acme%2fproject.git", ""},
		{"http://localhost:8080/github.com/acme/project.git?token=no", ""},
	} {
		t.Run(tt.input, func(t *testing.T) {
			u, err := url.Parse(tt.input)
			require.NoError(t, err)
			want := tt.want
			if want == "" {
				want = tt.input
			}
			assert.Equal(t, want, translator.Translate(u).String())
			assert.Equal(t, tt.input, u.String(), "original Git URL must be preserved")
		})
	}
}
