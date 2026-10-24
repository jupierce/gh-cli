package workspace_test

import (
	"context"
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

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func socketClient(t *testing.T, handler http.HandlerFunc) (*http.Client, *workspace.Client) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("workspace socket tests require Unix")
	}
	dir, err := os.MkdirTemp("", "gh-ws-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	socket := filepath.Join(dir, "api.sock")
	listener, err := net.Listen("unix", socket)
	require.NoError(t, err)
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	httpClient, err := api.NewHTTPClient(api.HTTPClientOptions{UnixDomainSocket: socket})
	require.NoError(t, err)
	w := workspace.Wrap(httpClient, &workspace.Config{Protocol: workspace.Protocol, Host: "github.com", Socket: socket})
	return httpClient, w
}

func capabilities(w http.ResponseWriter, operations ...string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(workspace.Capabilities{Protocol: workspace.Protocol, Ready: true, Host: "github.com", Generation: "generation-1", Login: "chai-bot", Operations: operations})
}

func TestSocketTransport(t *testing.T) {
	var handshakes atomic.Int32
	var requests atomic.Int32
	client, _ := socketClient(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Empty(t, r.Header.Get("Proxy-Authorization"))
		assert.Empty(t, r.Header.Get("Cookie"))
		if r.URL.Path == "/_chai/capabilities" {
			handshakes.Add(1)
			capabilities(w)
			return
		}
		assert.Equal(t, "generation-1", r.Header.Get("X-Chai-Workspace-Generation"))
		assert.Equal(t, workspace.Protocol, r.Header.Get("X-Chai-GH-Protocol"))
		requests.Add(1)
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "https://api.github.com/user", http.StatusFound)
		case "/graphql":
			fmt.Fprint(w, `{"data":{"viewer":{"login":"chai-bot"}}}`)
		default:
			fmt.Fprint(w, `{"login":"chai-bot"}`)
		}
	})
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("GH_TOKEN", "fixture-token-must-not-leave")
	for _, endpoint := range []string{"https://api.github.com/user", "https://github.com/user", "https://api.github.com/redirect"} {
		req, err := http.NewRequest(http.MethodGet, endpoint, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer fixture")
		req.Header.Set("Proxy-Authorization", "Basic fixture")
		req.Header.Set("Cookie", "fixture=value")
		req.Header.Set("X-Chai-Workspace-Generation", "spoofed")
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.JSONEq(t, `{"login":"chai-bot"}`, string(body))
	}
	var data struct{ Viewer struct{ Login string } }
	require.NoError(t, api.NewClientFromHTTP(client).GraphQL("github.com", "query { viewer { login } }", nil, &data))
	assert.Equal(t, "chai-bot", data.Viewer.Login)
	assert.EqualValues(t, 1, handshakes.Load())
	assert.EqualValues(t, 5, requests.Load())
	for _, endpoint := range []string{"https://example.com/user", "https://api.github.com:443/user", "https://user:password@api.github.com/user"} {
		_, err := client.Get(endpoint)
		require.ErrorContains(t, err, "CHAI_GH_UNSUPPORTED")
	}
	assert.EqualValues(t, 5, requests.Load())
}

func TestOperationNoFallbackOrRetry(t *testing.T) {
	for _, mode := range []string{"success", "denied", "approval", "redirect", "accepted", "pending", "malformed", "missing-result", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			var writes atomic.Int32
			_, client := socketClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/_chai/capabilities" {
					capabilities(w, "issue.create")
					return
				}
				writes.Add(1)
				assert.Equal(t, "/_chai/operations/issue.create", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "generation-1", r.Header.Get("X-Chai-Workspace-Generation"))
				data, _ := io.ReadAll(r.Body)
				assert.JSONEq(t, `{"parameters":{"title":"hello"}}`, string(data))
				switch mode {
				case "denied", "approval":
					w.WriteHeader(http.StatusForbidden)
					code := "CHAI_GH_DENIED"
					if mode == "approval" {
						code = "CHAI_GH_APPROVAL_REQUIRED"
					}
					fmt.Fprintf(w, `{"error":{"code":%q,"message":"use the parent persona"}}`, code)
				case "redirect":
					http.Redirect(w, r, "https://api.github.com/other", http.StatusTemporaryRedirect)
				case "accepted":
					w.WriteHeader(http.StatusAccepted)
					fmt.Fprint(w, `{"status":"completed","result":{}}`)
				case "pending":
					fmt.Fprint(w, `{"status":"pending","result":{}}`)
				case "malformed":
					fmt.Fprint(w, `{`)
				case "missing-result":
					fmt.Fprint(w, `{}`)
				case "disconnect":
					conn, _, err := w.(http.Hijacker).Hijack()
					if assert.NoError(t, err) {
						_ = conn.Close()
					}
				default:
					fmt.Fprint(w, `{"status":"completed","result":{"url":"https://github.com/acme/project/issues/1"}}`)
				}
			})
			var result struct{ URL string }
			err := client.Operation(context.Background(), "issue.create", map[string]string{"title": "hello"}, &result)
			if mode == "success" {
				require.NoError(t, err)
				assert.NotEmpty(t, result.URL)
			} else {
				require.Error(t, err)
			}
			if mode == "denied" || mode == "approval" {
				assert.Contains(t, err.Error(), "use the parent persona")
			}
			if mode == "disconnect" {
				assert.Contains(t, err.Error(), "outcome unknown")
			}
			assert.EqualValues(t, 1, writes.Load())
			err = client.Operation(context.Background(), "unadvertised", nil, nil)
			require.ErrorContains(t, err, "CHAI_GH_UNSUPPORTED")
			assert.EqualValues(t, 1, writes.Load())
		})
	}
}

func TestStaleGenerationAndCancellation(t *testing.T) {
	var handshakes atomic.Int32
	var operations atomic.Int32
	_, client := socketClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_chai/capabilities" {
			handshakes.Add(1)
			capabilities(w, "issue.create")
			return
		}
		operations.Add(1)
		assert.Equal(t, "generation-1", r.Header.Get("X-Chai-Workspace-Generation"))
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":{"code":"CHAI_GH_NOT_READY","message":"workspace generation changed; start a new command"}}`)
	})
	err := client.Operation(context.Background(), "issue.create", map[string]string{"title": "Test"}, nil)
	require.ErrorContains(t, err, "workspace generation changed")
	assert.EqualValues(t, 1, handshakes.Load(), "do not refresh and replay a write automatically")
	assert.EqualValues(t, 1, operations.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.Operation(ctx, "issue.create", nil, nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.EqualValues(t, 1, operations.Load())
}

func TestHandshakeFailureStopsRequests(t *testing.T) {
	for _, response := range []string{`{"protocol":"old"}`, `{"protocol":"chai.github-proxy/v1","ready":false,"host":"github.com","generation":"g","login":"bot"}`, `{`, strings.Repeat("x", 1024*1024+1)} {
		var calls atomic.Int32
		client, _ := socketClient(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			assert.Equal(t, "/_chai/capabilities", r.URL.Path)
			fmt.Fprint(w, response)
		})
		_, err := client.Get("https://api.github.com/user")
		require.Error(t, err)
		_, err = client.Get("https://api.github.com/user")
		require.Error(t, err)
		assert.EqualValues(t, 1, calls.Load())
	}
}
