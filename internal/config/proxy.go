package config

import (
	"errors"
	"fmt"
	"net/url"
)

// Proxies is what the proxy keys tell an HTTP client of the model's API.
type Proxies struct {
	// Set is false when no key is: the client goes by the environment
	// then, as it did before the keys.
	Set bool
	// HTTP and HTTPS are the proxies of the two schemes, all_proxy where
	// the scheme has none of its own; "" is none.
	HTTP, HTTPS string
	NoProxy     string
}

// Proxy is what the proxy keys of c tell the client. A key set to "" is
// one not set, as for curl: all_proxy stands in for it.
func (c Config) Proxy() Proxies {
	p := Proxies{
		Set:  c.HTTPProxy != "" || c.HTTPSProxy != "" || c.AllProxy != "" || c.NoProxy != "",
		HTTP: c.HTTPProxy, HTTPS: c.HTTPSProxy, NoProxy: c.NoProxy,
	}
	if p.HTTP == "" {
		p.HTTP = c.AllProxy
	}
	if p.HTTPS == "" {
		p.HTTPS = c.AllProxy
	}
	return p
}

// checkProxies rejects a proxy that net/http cannot speak to, or that is
// no URL at all, which the client would quietly take for a host. prefix
// goes before the key in the error ("profiles.work."); nil is a key not
// set. The value is not quoted: it may hold a password.
func checkProxies(prefix string, http, https, all *string) error {
	for _, k := range []struct {
		key string
		v   *string
	}{{"http_proxy", http}, {"https_proxy", https}, {"all_proxy", all}} {
		if k.v == nil || *k.v == "" {
			continue
		}
		if err := checkProxy(*k.v); err != nil {
			return fmt.Errorf("%s%s: %w", prefix, k.key, err)
		}
	}
	return nil
}

func checkProxy(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("not a URL such as http://host:port")
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
		return nil
	}
	return fmt.Errorf("scheme %q: want http, https, socks5 or socks5h", u.Scheme)
}
