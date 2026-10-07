package llm

import (
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/GoldenDeals/aish/internal/config"
)

// responseHeaderTimeout is the one the SDKs give their own clients: a
// server that took the request and never answers fails the request
// instead of hanging it. The body of a stream is not bounded by it.
const responseHeaderTimeout = 10 * time.Minute

// httpClient is the client of the requests to the model through the
// proxies of cfg, or nil when it sets none: the SDK's own then, which goes
// by the environment of the process. Once one is set, the environment is
// not read at all, lest a proxy left in .bashrc mix with the config's.
// no_proxy goes as http_proxy's environment variable does: hosts, domains,
// CIDR, ports; loopback is never proxied.
func httpClient(cfg config.Config) *http.Client {
	p := cfg.Proxy()
	if !p.Set {
		return nil
	}
	proxy := (&httpproxy.Config{HTTPProxy: p.HTTP, HTTPSProxy: p.HTTPS, NoProxy: p.NoProxy}).ProxyFunc()
	t := &http.Transport{}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		t = dt.Clone()
	}
	t.Proxy = func(r *http.Request) (*url.URL, error) { return proxy(r.URL) }
	t.ResponseHeaderTimeout = responseHeaderTimeout
	return &http.Client{Transport: t}
}
