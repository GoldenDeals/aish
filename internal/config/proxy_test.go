package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const proxies = `
http_proxy = "http://127.0.0.1:8080"
https_proxy = "http://u:p4ss@127.0.0.1:8080"
all_proxy = "socks5h://127.0.0.1:1080"
no_proxy = "localhost,.f6.dev,10.0.0.0/8"

[profiles.work]
base_url = "http://work.example"

[profiles.socks]
http_proxy = "socks5://10.1.1.1:1080"
https_proxy = ""
all_proxy = ""

[profiles.direct]
http_proxy = ""
https_proxy = ""
all_proxy = ""
`

func TestProxyKeys(t *testing.T) {
	cfg, err := load(t, proxies)
	if err != nil {
		t.Fatal(err)
	}
	want := Proxies{Set: true, HTTP: "http://127.0.0.1:8080", HTTPS: "http://u:p4ss@127.0.0.1:8080", NoProxy: "localhost,.f6.dev,10.0.0.0/8"}
	if got := cfg.Proxy(); got != want {
		t.Errorf("top level: %+v, want %+v", got, want)
	}

	// A profile naming its own endpoint keeps the user's network: the
	// proxies are not the endpoint's, as its key is.
	work, err := LoadProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	if got := work.Proxy(); got != want {
		t.Errorf("work, no proxy keys of its own: %+v, want the top level's %+v", got, want)
	}

	socks, err := LoadProfile("socks")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := socks.Proxy(), (Proxies{Set: true, HTTP: "socks5://10.1.1.1:1080", NoProxy: "localhost,.f6.dev,10.0.0.0/8"}); got != want {
		t.Errorf("socks: %+v, want %+v", got, want)
	}

	// Set to "" in the profile, the proxies are off for its endpoint;
	// no_proxy still keeps the environment out.
	direct, err := LoadProfile("direct")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := direct.Proxy(), (Proxies{Set: true, NoProxy: "localhost,.f6.dev,10.0.0.0/8"}); got != want {
		t.Errorf("direct: %+v, want %+v", got, want)
	}
}

// all_proxy stands in for a scheme without a proxy of its own, and a key
// set to "" is not set, as for curl.
func TestProxyAll(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want Proxies
	}{
		{Config{}, Proxies{}},
		{Config{AllProxy: "socks5://h:1"}, Proxies{Set: true, HTTP: "socks5://h:1", HTTPS: "socks5://h:1"}},
		{Config{HTTPProxy: "http://h:2", AllProxy: "socks5://h:1"}, Proxies{Set: true, HTTP: "http://h:2", HTTPS: "socks5://h:1"}},
		{Config{HTTPSProxy: "http://h:3"}, Proxies{Set: true, HTTPS: "http://h:3"}},
		{Config{NoProxy: "*"}, Proxies{Set: true, NoProxy: "*"}},
	} {
		if got := tc.cfg.Proxy(); got != tc.want {
			t.Errorf("%+v: %+v, want %+v", tc.cfg, got, tc.want)
		}
	}
}

func TestProxyErrors(t *testing.T) {
	for _, tc := range []struct{ toml, want string }{
		{`http_proxy = "ftp://h:21"`, `http_proxy: scheme "ftp": want http, https, socks5 or socks5h`},
		{`https_proxy = "127.0.0.1:8080"`, "https_proxy: not a URL"},
		{`all_proxy = "localhost:1080"`, "all_proxy: not a URL"},
		{`http_proxy = "http://"`, "http_proxy: not a URL"},
		{`all_proxy = "http://u:s3cr3t@h:1 x"`, "all_proxy: not a URL"},
		{"[profiles.x]\nhttp_proxy = \"gopher://h\"\n", `profiles.x.http_proxy: scheme "gopher"`},
		{"[profiles.x]\nall_proxy = \"h\"\n", "profiles.x.all_proxy: not a URL"},
	} {
		_, err := load(t, tc.toml)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "config.toml: ") {
			t.Errorf("%q: %v, want %q and the path", tc.toml, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t") {
			t.Errorf("%q: the password is in the error: %v", tc.toml, err)
		}
	}
	for _, ok := range []string{"http://h:1", "https://h", "socks5://u:p@h:1080", "socks5h://h:1080", "HTTP://H:1"} {
		if _, err := load(t, `http_proxy = "`+ok+`"`); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

// A repository does not choose where the requests go: the proxies stay in
// config.toml.
func TestProxyNotInProject(t *testing.T) {
	for _, key := range []string{"http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		root := t.TempDir()
		t.Setenv("HOME", filepath.Join(root, "home"))
		repo(t, root, key+` = "http://evil:8080"`+"\n")
		want := `key "` + key + `" is not allowed in a project config`
		if _, _, err := Project(Default(), root); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", key, err, want)
		}
	}
}
