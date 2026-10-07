package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/inebotov/aish/internal/config"
)

// The target of the requests: not a loopback name, which is never proxied,
// and not one that resolves, so that a request going round the proxy fails.
const target = "http://aish.test"

// proxyServer is an HTTP proxy that answers every request itself, with a
// reply of the Anthropic API, and tells the hosts it was asked for.
func proxyServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu    sync.Mutex
		hosts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.URL.Host)
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, anthropicStream)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hosts...)
	}
}

// noEnvProxy keeps the proxies of the environment the tests run in out of
// them.
func noEnvProxy(t *testing.T) {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(k, "")
	}
}

func TestHTTPClientNone(t *testing.T) {
	if c := httpClient(config.Config{BaseURL: target, APIKey: "k"}); c != nil {
		t.Errorf("no proxy keys: %v, want nil, the SDK's own client", c)
	}
}

// A request of the Anthropic client goes through http_proxy, and through
// it alone: the environment is not read once the config sets a proxy.
func TestHTTPClientProxy(t *testing.T) {
	noEnvProxy(t)
	srv, hosts := proxyServer(t)
	env, envHosts := proxyServer(t)
	t.Setenv("HTTP_PROXY", env.URL)
	t.Setenv("http_proxy", env.URL)

	p := newAnthropic(config.Config{Model: "m", APIKey: "k", BaseURL: target, HTTPProxy: srv.URL})
	resp, err := p.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Text: "hi"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "done" {
		t.Errorf("reply %q, want the proxy's", resp.Text)
	}
	if got := hosts(); len(got) != 1 || got[0] != "aish.test" {
		t.Errorf("the proxy was asked for %q, want aish.test once", got)
	}
	if got := envHosts(); len(got) != 0 {
		t.Errorf("the proxy of the environment was asked for %q", got)
	}
}

// proxyFor is the proxy the client of cfg takes for raw.
func proxyFor(t *testing.T, cfg config.Config, raw string) string {
	t.Helper()
	c := httpClient(cfg)
	if c == nil {
		t.Fatalf("%+v: no client", cfg)
	}
	req, err := http.NewRequest("GET", raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.Transport.(*http.Transport).Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		return ""
	}
	return u.String()
}

func TestHTTPClientRules(t *testing.T) {
	noEnvProxy(t)
	t.Setenv("HTTPS_PROXY", "http://env:1")
	t.Setenv("https_proxy", "http://env:1")
	const (
		httpP  = "http://p:8080"
		httpsP = "http://u:pw@s:8080"
		all    = "socks5h://a:1080"
	)
	for _, tc := range []struct {
		name string
		cfg  config.Config
		url  string
		want string
	}{
		{"http", config.Config{HTTPProxy: httpP, HTTPSProxy: httpsP}, "http://aish.test/v1", httpP},
		{"https", config.Config{HTTPProxy: httpP, HTTPSProxy: httpsP}, "https://api.aish.test/v1", httpsP},
		// The environment's HTTPS_PROXY does not stand in for the key not set.
		{"https not set", config.Config{HTTPProxy: httpP}, "https://api.aish.test/v1", ""},
		{"all for https", config.Config{HTTPProxy: httpP, AllProxy: all}, "https://api.aish.test/v1", all},
		{"all for http", config.Config{AllProxy: all}, "http://aish.test/v1", all},
		{"own over all", config.Config{HTTPProxy: httpP, AllProxy: all}, "http://aish.test/v1", httpP},
		{"no_proxy host", config.Config{AllProxy: all, NoProxy: "aish.test"}, "https://aish.test/v1", ""},
		{"no_proxy subdomain", config.Config{AllProxy: all, NoProxy: "aish.test"}, "https://api.aish.test/v1", ""},
		{"no_proxy dot", config.Config{AllProxy: all, NoProxy: ".aish.test"}, "https://aish.test/v1", all},
		{"no_proxy other", config.Config{AllProxy: all, NoProxy: "localhost,other.test"}, "https://aish.test/v1", all},
		{"no_proxy cidr", config.Config{AllProxy: all, NoProxy: "10.0.0.0/8"}, "https://10.1.2.3/v1", ""},
		{"no_proxy port", config.Config{AllProxy: all, NoProxy: "aish.test:8443"}, "https://aish.test/v1", all},
		{"no_proxy star", config.Config{AllProxy: all, NoProxy: "*"}, "https://aish.test/v1", ""},
		{"loopback", config.Config{AllProxy: all}, "http://127.0.0.1:11434/v1", ""},
		{"localhost", config.Config{AllProxy: all}, "http://localhost:11434/v1", ""},
		// no_proxy alone: no proxy, and the environment's is not read.
		{"no_proxy alone", config.Config{NoProxy: "localhost"}, "https://aish.test/v1", ""},
	} {
		if got := proxyFor(t, tc.cfg, tc.url); got != tc.want {
			t.Errorf("%s: %s goes through %q, want %q", tc.name, tc.url, got, tc.want)
		}
	}
}

// The proxy's client keeps what the SDKs give their own.
func TestHTTPClientTransport(t *testing.T) {
	c := httpClient(config.Config{HTTPProxy: "http://p:1"})
	tr := c.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != responseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout %v, want %v", tr.ResponseHeaderTimeout, responseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout == 0 || !tr.ForceAttemptHTTP2 {
		t.Errorf("not a clone of http.DefaultTransport: %+v", tr)
	}
	// The proxy of the transport is the config's, not the environment's.
	u, _ := url.Parse("http://p:1")
	if got, _ := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "http", Host: "aish.test"}}); got == nil || *got != *u {
		t.Errorf("proxy %v, want %v", got, u)
	}
}
