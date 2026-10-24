package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Capabilities is the manager's live identity and supported operation set.
type Capabilities struct {
	Protocol   string   `json:"protocol"`
	Ready      bool     `json:"ready"`
	Generation string   `json:"generation"`
	Host       string   `json:"host"`
	Login      string   `json:"login"`
	Operations []string `json:"operations"`
}

// Error preserves a manager error code without presenting failed writes as success.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Unsupported describes a feature outside the workspace contract.
func Unsupported(message string) error { return &Error{Code: "CHAI_GH_UNSUPPORTED", Message: message} }

// Client uses an upstream HTTP-over-Unix-socket transport. It never resolves credentials.
type Client struct {
	cfg  *Config
	http *http.Client
	once sync.Once
	caps *Capabilities
	err  error
}

type transport struct {
	client *Client
	base   http.RoundTripper
}

// Wrap routes all requests through the provided socket transport and strips credentials
// before they reach its logging layers. The caller must configure that transport's socket.
func Wrap(httpClient *http.Client, cfg *Config) *Client {
	c := &Client{cfg: cfg}
	base := httpClient.Transport
	c.http = &http.Client{Transport: credentialFree{base}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.Bind(httpClient)
	return c
}

// SocketPath returns the public socket path for another upstream HTTP client.
func (c *Client) SocketPath() string { return c.cfg.Socket }

// Bind applies the same handshake and credential stripping to another client
// that has been configured with SocketPath. This preserves gh api's logging options.
func (c *Client) Bind(httpClient *http.Client) {
	httpClient.Transport = &transport{client: c, base: credentialFree{httpClient.Transport}}
}

// FromHTTP returns the workspace client attached by the factory, or nil in ordinary mode.
func FromHTTP(c *http.Client) *Client {
	if c == nil {
		return nil
	}
	if t, ok := c.Transport.(*transport); ok {
		return t.client
	}
	return nil
}

type credentialFree struct{ base http.RoundTripper }

func (t credentialFree) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.User != nil || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || (req.URL.Host != "api.github.com" && req.URL.Host != "github.com") || (req.Host != "" && req.Host != req.URL.Host) {
		return nil, Unsupported("request destination is outside the workspace GitHub API contract")
	}
	r := req.Clone(req.Context())
	r.Header.Del("Authorization")
	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Cookie")
	return t.base.RoundTrip(r)
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	caps, err := t.client.Capabilities(req.Context())
	if err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("X-Chai-Workspace-Generation", caps.Generation)
	r.Header.Set("X-Chai-GH-Protocol", Protocol)
	return t.base.RoundTrip(r)
}

// Capabilities checks readiness once per client. The manager must check generation and
// authorization on every subsequent request, including those from unmodified clients.
func (c *Client) Capabilities(ctx context.Context) (*Capabilities, error) {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		var caps Capabilities
		c.err = c.request(ctx, http.MethodGet, "/_chai/capabilities", nil, "", &caps)
		if c.err != nil {
			return
		}
		if caps.Protocol != Protocol || caps.Host != c.cfg.Host || caps.Generation == "" || caps.Login == "" {
			c.err = fmt.Errorf("incompatible or incomplete workspace capabilities")
			return
		}
		if !caps.Ready {
			c.err = &Error{Code: "CHAI_GH_NOT_READY", Message: "workspace manager is not ready"}
			return
		}
		c.caps = &caps
	})
	return c.caps, c.err
}

// Operation submits a declared typed operation exactly once. Neither redirect following
// nor retry is permitted: a lost response may follow a completed write.
func (c *Client) Operation(ctx context.Context, name string, parameters any, result any) error {
	caps, err := c.Capabilities(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(caps.Operations, name) {
		return Unsupported("manager does not advertise " + name)
	}
	if strings.ContainsAny(name, "/?#") {
		return Unsupported("invalid operation name")
	}
	body, err := json.Marshal(map[string]any{"parameters": parameters})
	if err != nil {
		return err
	}
	var response struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	}
	if err := c.request(ctx, http.MethodPost, "/_chai/operations/"+name, body, caps.Generation, &response); err != nil {
		return err
	}
	if response.Status != "completed" || len(response.Result) == 0 || response.Result[0] != '{' {
		return fmt.Errorf("workspace operation %s did not confirm completion; check its outcome before retrying", name)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("invalid workspace operation result; check its outcome before retrying: %w", err)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, endpoint string, body []byte, generation string, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Chai-GH-Protocol", Protocol)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if generation != "" {
		req.Header.Set("X-Chai-Workspace-Generation", generation)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if method == http.MethodPost {
			return fmt.Errorf("workspace operation outcome unknown; check before retrying: %w", err)
		}
		return fmt.Errorf("workspace manager unavailable: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return fmt.Errorf("read workspace response (check write outcome before retrying): %w", err)
	}
	if len(data) > 1024*1024 {
		return fmt.Errorf("workspace response exceeds 1 MiB; check write outcome before retrying")
	}
	if resp.StatusCode != http.StatusOK {
		var envelope struct {
			Error *Error `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil && envelope.Error != nil && envelope.Error.Code != "" {
			return envelope.Error
		}
		return fmt.Errorf("workspace manager returned HTTP %d; check write outcome before retrying", resp.StatusCode)
	}
	if err := json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("invalid workspace response; check write outcome before retrying: %w", err)
	}
	return nil
}
