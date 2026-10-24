package root

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cli/cli/v2/internal/config"
	"github.com/cli/cli/v2/internal/gh"
	"github.com/cli/cli/v2/internal/telemetry"
	"github.com/cli/cli/v2/internal/workspace"
	"github.com/cli/cli/v2/pkg/cmd/factory"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workspaceCommand(t *testing.T, handler http.HandlerFunc) func(...string) (string, string, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("workspace requires Unix sockets")
	}
	dir, err := os.MkdirTemp("", "gh-cmd-ws-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	t.Setenv("GH_CONFIG_DIR", dir)
	t.Setenv("GH_HOST", "github.com")
	t.Setenv("GH_REPO", "")
	t.Setenv("GH_TOKEN", "fixture-token-never-forward")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(dir, "no-keyring"))
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("GH_DEBUG", "")
	socket := filepath.Join(dir, "api.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Equal(t, "api.github.com", r.Host)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/_chai/capabilities" {
			_ = json.NewEncoder(w).Encode(workspace.Capabilities{Protocol: workspace.Protocol, Ready: true, Host: "github.com", Login: "chai-bot", Generation: "g1", Operations: []string{"pull_request.create", "pull_request.edit", "issue.create", "issue.edit", "comment.create", "pull_request.review", "repository.fork"}})
			return
		}
		assert.Equal(t, "g1", r.Header.Get("X-Chai-Workspace-Generation"))
		handler(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	descriptor, err := json.Marshal(workspace.Config{Protocol: workspace.Protocol, Host: "github.com", Socket: socket, GitProxyPrefixes: []string{"http://localhost:8080/github.com/"}})
	require.NoError(t, err)
	filename := filepath.Join(dir, "workspace.json")
	require.NoError(t, os.WriteFile(filename, descriptor, 0600))
	t.Setenv("GH_WORKSPACE_CONFIG", filename)
	return func(args ...string) (string, string, error) {
		ios, stdin, stdout, stderr := iostreams.Test()
		f := factory.New("test+chai.1", "", func() (gh.Config, error) { return config.NewMockConfig(), nil }, ios, "gh", nil)
		cmd, err := NewCmdRoot(f, &telemetry.NoOpService{}, "test+chai.1", "")
		require.NoError(t, err)
		cmd.SetArgs(args)
		cmd.SetIn(stdin)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		_, err = cmd.ExecuteC()
		return stdout.String(), stderr.String(), err
	}
}

func TestWorkspaceCommands(t *testing.T) {
	var apiCalls atomic.Int32
	run := workspaceCommand(t, func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		switch r.URL.Path {
		case "/user":
			fmt.Fprint(w, `{"login":"chai-bot"}`)
		case "/items":
			if r.URL.Query().Get("page") == "2" {
				fmt.Fprint(w, `[{"page":2}]`)
			} else {
				w.Header().Set("Link", `<https://api.github.com/items?page=2>; rel="next"`)
				fmt.Fprint(w, `[{"page":1}]`)
			}
		case "/graphql":
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"id":"PR_42","number":42,"title":"Workspace PR","url":"https://github.com/acme/project/pull/42"}}}}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(500)
		}
	})
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"api", "user", "--jq", ".login", "-H", "Authorization: Bearer fixture"}, "chai-bot\n"},
		{[]string{"api", "https://api.github.com/user", "--template", "{{.login}}"}, "chai-bot"},
		{[]string{"api", "items", "--paginate", "--jq", ".[].page"}, "1\n2\n"},
		{[]string{"pr", "view", "42", "-R", "acme/project", "--json", "number,title", "--jq", ".title"}, "Workspace PR\n"},
		{[]string{"auth", "status", "--json", "hosts", "--jq", ".hosts[\"github.com\"][0].tokenSource"}, "workspace-manager\n"},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			stdout, stderr, err := run(tt.args...)
			require.NoError(t, err)
			assert.Equal(t, tt.want, stdout)
			assert.Empty(t, stderr)
		})
	}
	assert.EqualValues(t, 5, apiCalls.Load())
	stdout, stderr, err := run("auth", "status", "--show-token")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Workspace manager identity: chai-bot")
	assert.NotContains(t, stdout, "fixture-token")
	assert.Empty(t, stderr)
}

func TestWorkspaceRejectsUnsupportedBeforeEffects(t *testing.T) {
	var calls atomic.Int32
	run := workspaceCommand(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); t.Errorf("unexpected request: %s", r.URL) })
	for _, args := range [][]string{
		{"auth", "token"}, {"auth", "login"}, {"auth", "setup-git"},
		{"pr", "merge", "42", "-R", "acme/project"},
		{"pr", "create", "-R", "acme/project", "--title", "Test", "--body", "Body"},
		{"pr", "create", "-R", "acme/project", "--title", "Test", "--body", "Body", "--head", "bot:topic", "--base", "main", "--label", "bug"},
		{"pr", "review", "42", "-R", "acme/project", "--approve", "--body", "Body"},
		{"pr", "comment", "42", "-R", "acme/project", "--body", "Body", "--edit-last"},
		{"pr", "edit", "42", "-R", "acme/project", "--base", "other"},
		{"issue", "create", "-R", "acme/project", "--title", "Test", "--body", "Body", "--assignee", "me"},
		{"api", "user", "--cache", "1m"},
		{"skills", "list"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, _, err := run(args...)
			require.ErrorContains(t, err, "CHAI_GH_UNSUPPORTED")
			assert.Empty(t, stdout)
		})
	}
	assert.Zero(t, calls.Load())
}
