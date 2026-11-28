package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/runtimepkg"
)

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func envKeyCount(env []string, key string) int {
	prefix := key + "="
	n := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			n++
		}
	}
	return n
}

// unsetEnv removes keys from the process environment for the duration of the test and restores prior values/presence.
func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	type saved struct {
		val string
		ok  bool
	}
	prev := make(map[string]saved, len(keys))
	for _, k := range keys {
		v, ok := os.LookupEnv(k)
		prev[k] = saved{val: v, ok: ok}
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("Unsetenv(%s): %v", k, err)
		}
	}
	t.Cleanup(func() {
		for _, k := range keys {
			s := prev[k]
			if s.ok {
				_ = os.Setenv(k, s.val)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	})
}

func TestParseScutilProxy(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want systemProxy
	}{
		{
			name: "http https socks with exceptions and pac disabled",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : 192.0.2.0/24
    2 : 198.51.100/24
    3 : localhost
    4 : *.local
    5 : *.example.com
    6 : <local>
  }
  ExcludeSimpleHostnames : 1
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  ProxyAutoDiscoveryEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 7891
  SOCKSProxy : 127.0.0.1
}
`,
			want: systemProxy{
				HTTP:                   "http://127.0.0.1:7890",
				HTTPS:                  "http://127.0.0.1:7890",
				SOCKS:                  "socks5h://127.0.0.1:7891",
				Exceptions:             []string{"127.0.0.1", "192.0.2.0/24", "198.51.100.0/24", "localhost", ".local", ".example.com"},
				ExcludeSimpleHostnames: true,
			},
		},
		{
			name: "socks only ignores disabled http and https with stale host and port",
			out: `<dictionary> {
  HTTPEnable : 0
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : proxy.example.com
}
`,
			want: systemProxy{
				SOCKS: "socks5h://proxy.example.com:1080",
			},
		},
		{
			name: "ipv6 proxies including bare bracketed and link-local zone",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : [::1]
    1 : 2001:db8::/32
    2 : [2001:db8::5]:8080
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : ::1
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : [2001:db8::1]
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : fe80::1%en0
}
`,
			want: systemProxy{
				HTTP:       "http://[::1]:7890",
				HTTPS:      "http://[2001:db8::1]:8443",
				SOCKS:      "socks5h://[fe80::1%25en0]:1080",
				Exceptions: []string{"::1", "2001:db8::/32", "[2001:db8::5]:8080"},
			},
		},
		{
			name: "all disabled returns no active proxy",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPEnable : 0
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 0
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  ProxyAutoDiscoveryEnable : 0
  SOCKSEnable : 0
  SOCKSPort : 7890
  SOCKSProxy : 127.0.0.1
}
`,
			want: systemProxy{
				Exceptions: []string{".local"},
			},
		},
		{
			name: "pac enabled without static proxies returns empty",
			out: `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 0
  ProxyAutoConfigEnable : 1
  ProxyAutoConfigURLString : http://proxy.example.com/proxy.pac
}
`,
			want: systemProxy{},
		},
		{
			name: "pac enabled alongside manual proxies skips manual injection",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7890
  SOCKSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 1
  ProxyAutoConfigURLString : http://proxy.example.com/proxy.pac
}
`,
			want: systemProxy{},
		},
		{
			name: "wpad enabled alongside manual proxies skips manual injection",
			out: `<dictionary> {
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  ProxyAutoDiscoveryEnable : 1
}
`,
			want: systemProxy{},
		},
		{
			name: "drops bare exact domains while keeping wildcard suffixes ips cidrs and localhost",
			out: `<dictionary> {
  ExceptionsList : <array> {
    0 : example.org
    1 : api.example.com
    2 : example.org:8443
    3 : intranet
    4 : *.example.org
    5 : .example.net
    6 : *.example.org:8443
    7 : localhost
    8 : localhost:8080
    9 : 127.0.0.1:9000
    10 : 192.0.2.0/24
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
}
`,
			want: systemProxy{
				HTTP:       "http://127.0.0.1:7890",
				Exceptions: []string{".example.org", ".example.net", ".example.org:8443", "localhost", "localhost:8080", "127.0.0.1:9000", "192.0.2.0/24"},
			},
		},
		{
			name: "ignores nested scoped dictionary and unrelated arrays",
			out: `<dictionary> {
  OtherList : <array> {
    0 : *.unrelated.example.com
  }
  ExceptionsList : <array> {
    0 : *.example.org
  }
  __SCOPED__ : <dictionary> {
    en0 : <dictionary> {
      HTTPEnable : 0
      HTTPPort : 9999
      HTTPProxy : 203.0.113.9
      ExceptionsList : <array> {
        0 : *.scoped.example.com
      }
    }
  }
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
}
`,
			want: systemProxy{
				HTTP:       "http://127.0.0.1:7890",
				Exceptions: []string{".example.org"},
			},
		},
		{
			name: "filters invalid ports hosts and malformed exceptions without dropping valid peers",
			out: "<dictionary> {\r\n" +
				"  ExceptionsList : <array> {\r\n" +
				"    0 : *.local\r\n" +
				"    1 : <local>\r\n" +
				"    2 : *.\r\n" +
				"    3 : foo*bar\r\n" +
				"    4 : bad host\r\n" +
				"    5 : a,b\r\n" +
				"    6 : example.com/path\r\n" +
				"    7 : 192.0.2.0/99\r\n" +
				"    8 : ctrl\x07host\r\n" +
				"    9 : localhost:8080\r\n" +
				"  }\r\n" +
				"  HTTPEnable : 1\r\n" +
				"  HTTPPort : 0\r\n" +
				"  HTTPProxy : 127.0.0.1\r\n" +
				"  HTTPSEnable : 1\r\n" +
				"  HTTPSPort : 8080\r\n" +
				"  HTTPSProxy : 127.0.0.1\r\n" +
				"  SOCKSEnable : 1\r\n" +
				"  SOCKSPort : 65536\r\n" +
				"  SOCKSProxy : 127.0.0.1\r\n" +
				"}\r\n",
			want: systemProxy{
				HTTPS:      "http://127.0.0.1:8080",
				Exceptions: []string{".local", "localhost:8080"},
			},
		},
		{
			name: "empty or garbage output returns empty",
			out:  "not a scutil dictionary\nHTTPEnable : 1\nHTTPProxy : 127.0.0.1\nHTTPPort : 7890\n",
			want: systemProxy{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseScutilProxy(tc.out)
			if got.HTTP != tc.want.HTTP ||
				got.HTTPS != tc.want.HTTPS ||
				got.SOCKS != tc.want.SOCKS ||
				got.ExcludeSimpleHostnames != tc.want.ExcludeSimpleHostnames ||
				!slices.Equal(got.Exceptions, tc.want.Exceptions) {
				t.Fatalf("parseScutilProxy() =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

func TestFormatProxyURLValidation(t *testing.T) {
	tests := []struct {
		name   string
		scheme string
		host   string
		port   string
		want   string
	}{
		{"ipv4", "http", "127.0.0.1", "7890", "http://127.0.0.1:7890"},
		{"hostname", "http", "proxy-1.example.com", "8080", "http://proxy-1.example.com:8080"},
		{"fqdn trailing dot", "http", "proxy.local.", "8080", "http://proxy.local.:8080"},
		{"ipv6 bare", "socks5h", "::1", "1080", "socks5h://[::1]:1080"},
		{"ipv6 bracketed", "http", "[2001:db8::1]", "7890", "http://[2001:db8::1]:7890"},
		{"ipv6 zone", "http", "fe80::1%en0", "7890", "http://[fe80::1%25en0]:7890"},
		{"port min", "http", "127.0.0.1", "1", "http://127.0.0.1:1"},
		{"port max", "http", "127.0.0.1", "65535", "http://127.0.0.1:65535"},
		{"port zero", "http", "127.0.0.1", "0", ""},
		{"port overflow", "http", "127.0.0.1", "65536", ""},
		{"port negative", "http", "127.0.0.1", "-1", ""},
		{"port plus sign", "http", "127.0.0.1", "+7890", ""},
		{"port leading zero", "http", "127.0.0.1", "07890", ""},
		{"port non-numeric", "http", "127.0.0.1", "abc", ""},
		{"empty host", "http", "", "7890", ""},
		{"host with scheme", "http", "http://127.0.0.1", "7890", ""},
		{"host with userinfo", "http", "user:pass@127.0.0.1", "7890", ""},
		{"host with path", "http", "127.0.0.1/path", "7890", ""},
		{"host with whitespace", "http", "proxy example.com", "7890", ""},
		{"host with control char", "http", "127.0.0.1\x00", "7890", ""},
		{"host with crlf", "http", "127.0.0.1\r\nX=1", "7890", ""},
		{"bracketed ipv4 rejected", "http", "[127.0.0.1]", "7890", ""},
		{"malformed ipv6", "http", "2001:db8:::1", "7890", ""},
		{"invalid ipv6 zone", "http", "fe80::1%en0/bad", "7890", ""},
		{"bogus numeric host", "http", "999.999.999.999", "7890", ""},
		{"leading hyphen host", "http", "-bad.example.com", "7890", ""},
		{"empty label host", "http", "bad..example.com", "7890", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatProxyURL(tc.scheme, tc.host, tc.port); got != tc.want {
				t.Fatalf("formatProxyURL(%q, %q, %q) = %q, want %q", tc.scheme, tc.host, tc.port, got, tc.want)
			}
		})
	}
}

func TestWithSystemProxyEnvRespectsExplicitEnv(t *testing.T) {
	oldDetect, oldShell := detectSystemProxy, detectShellProxy
	t.Cleanup(func() { detectSystemProxy, detectShellProxy = oldDetect, oldShell })
	detectShellProxy = func(context.Context) shellProxy {
		t.Error("detectShellProxy should not be called when explicit or system proxy applies")
		return shellProxy{}
	}

	for _, key := range proxyVars {
		t.Run(key+" non-empty", func(t *testing.T) {
			called := false
			detectSystemProxy = func(context.Context) systemProxy {
				called = true
				return systemProxy{
					HTTP:  "http://127.0.0.1:7890",
					HTTPS: "http://127.0.0.1:7890",
					SOCKS: "socks5h://127.0.0.1:7890",
				}
			}
			base := []string{"PATH=/usr/bin", key + "=http://proxy.example.com:8888"}
			got := withSystemProxyEnv(context.Background(), base)
			if called {
				t.Fatalf("detectSystemProxy was called despite explicit %s", key)
			}
			if !slices.Equal(got, base) {
				t.Fatalf("env changed despite explicit %s: got %v, want %v", key, got, base)
			}
		})

		t.Run(key+" explicit empty disables fallback", func(t *testing.T) {
			called := false
			detectSystemProxy = func(context.Context) systemProxy {
				called = true
				return systemProxy{
					HTTP:  "http://127.0.0.1:7890",
					HTTPS: "http://127.0.0.1:7890",
					SOCKS: "socks5h://127.0.0.1:7890",
				}
			}
			base := []string{"PATH=/usr/bin", key + "="}
			got := withSystemProxyEnv(context.Background(), base)
			if called {
				t.Fatalf("detectSystemProxy was called despite explicit empty %s=", key)
			}
			if !slices.Equal(got, base) {
				t.Fatalf("env changed despite explicit empty %s=: got %v, want %v", key, got, base)
			}
		})
	}

	t.Run("unset proxy vars falls back to system proxy", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return systemProxy{HTTPS: "http://127.0.0.1:7890"}
		}
		base := []string{"PATH=/usr/bin"}
		got := withSystemProxyEnv(context.Background(), base)
		if !called {
			t.Fatal("detectSystemProxy was not called when proxy vars were unset")
		}
		m := envMap(got)
		if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("system proxy not injected: %v", m)
		}
	})
}

func TestApplySystemProxyEnvMergesNoProxyAndSyncsCase(t *testing.T) {
	fullProxy := systemProxy{
		HTTP:       "http://127.0.0.1:7890",
		HTTPS:      "http://127.0.0.1:7890",
		SOCKS:      "socks5h://127.0.0.1:7890",
		Exceptions: []string{"127.0.0.1", "localhost", "192.0.2.0/24", ".local"},
	}

	t.Run("injects all enabled proxies and loopback-safe no_proxy", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		orig := slices.Clone(base)
		got := applySystemProxyEnv(base, fullProxy)
		if !slices.Equal(base, orig) {
			t.Fatalf("input slice mutated: %v", base)
		}
		m := envMap(got)
		for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
			if m[k] != "http://127.0.0.1:7890" {
				t.Fatalf("%s = %q, want http://127.0.0.1:7890", k, m[k])
			}
			if envKeyCount(got, k) != 1 {
				t.Fatalf("%s appeared %d times in %v", k, envKeyCount(got, k), got)
			}
		}
		for _, k := range []string{"ALL_PROXY", "all_proxy"} {
			if m[k] != "socks5h://127.0.0.1:7890" {
				t.Fatalf("%s = %q, want socks5h://127.0.0.1:7890", k, m[k])
			}
		}
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("merges existing uppercase and lowercase NO_PROXY without losing loopback", func(t *testing.T) {
		base := []string{"NO_PROXY=corp.example.com,127.0.0.1", "no_proxy=internal.example.org"}
		got := applySystemProxyEnv(base, fullProxy)
		m := envMap(got)
		wantNoProxy := "corp.example.com,127.0.0.1,internal.example.org,localhost,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("preserves wildcard NO_PROXY", func(t *testing.T) {
		base := []string{"NO_PROXY=*"}
		got := applySystemProxyEnv(base, fullProxy)
		m := envMap(got)
		if m["NO_PROXY"] != "*" || m["no_proxy"] != "*" {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want *", m["NO_PROXY"], m["no_proxy"])
		}
	})

	t.Run("exclude simple hostnames adds single-label gateway host to NO_PROXY", func(t *testing.T) {
		p := fullProxy
		p.ExcludeSimpleHostnames = true
		base := []string{"PATH=/usr/bin", "GOOGLE_GEMINI_BASE_URL=http://gateway:8080/v1beta"}
		got := applySystemProxyEnv(base, p)
		m := envMap(got)
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local,gateway"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("exclude simple hostnames ignores multi-label or ip gateway hosts", func(t *testing.T) {
		p := fullProxy
		p.ExcludeSimpleHostnames = true
		for _, baseURL := range []string{"https://gateway.example.com", "http://192.0.2.10:8080"} {
			got := applySystemProxyEnv([]string{"GOOGLE_GEMINI_BASE_URL=" + baseURL}, p)
			m := envMap(got)
			wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
			if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
				t.Fatalf("baseURL=%q: NO_PROXY=%q no_proxy=%q, want %q", baseURL, m["NO_PROXY"], m["no_proxy"], wantNoProxy)
			}
		}
	})

	t.Run("exclude simple hostnames off does not add single-label gateway host", func(t *testing.T) {
		base := []string{"PATH=/usr/bin", "GOOGLE_GEMINI_BASE_URL=http://gateway"}
		got := applySystemProxyEnv(base, fullProxy)
		m := envMap(got)
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("socks only does not set http or https proxy", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		got := applySystemProxyEnv(base, systemProxy{SOCKS: "socks5h://127.0.0.1:1080"})
		m := envMap(got)
		if m["ALL_PROXY"] != "socks5h://127.0.0.1:1080" || m["all_proxy"] != "socks5h://127.0.0.1:1080" {
			t.Fatalf("ALL_PROXY=%q all_proxy=%q", m["ALL_PROXY"], m["all_proxy"])
		}
		for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
			if _, ok := m[k]; ok {
				t.Fatalf("unexpected %s=%q in socks-only proxy env", k, m[k])
			}
		}
		if m["NO_PROXY"] != loopbackNoProxy || m["no_proxy"] != loopbackNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], loopbackNoProxy)
		}
	})

	t.Run("disabled system proxy leaves env untouched", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		got := applySystemProxyEnv(base, systemProxy{Exceptions: []string{".local"}})
		if !slices.Equal(got, base) {
			t.Fatalf("disabled system proxy modified env: got %v, want %v", got, base)
		}
	})
}

func TestAgyBridgeEnvSystemProxyIntegration(t *testing.T) {
	oldDetect, oldShell := detectSystemProxy, detectShellProxy
	t.Cleanup(func() { detectSystemProxy, detectShellProxy = oldDetect, oldShell })
	detectShellProxy = func(context.Context) shellProxy {
		t.Error("detectShellProxy should not be called when explicit or system proxy applies")
		return shellProxy{}
	}

	detected := systemProxy{
		HTTP:                   "http://127.0.0.1:7890",
		HTTPS:                  "http://127.0.0.1:7890",
		SOCKS:                  "socks5h://127.0.0.1:7890",
		Exceptions:             []string{"192.0.2.0/24", ".local"},
		ExcludeSimpleHostnames: true,
	}

	t.Run("injects system proxy when launchd env has no proxy", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy { return detected }
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		m := envMap(got)
		if m["PERSONA_RUNTIME_SOCKET"] != "/tmp/persona-runtime.sock" {
			t.Fatalf("PERSONA_RUNTIME_SOCKET = %q", m["PERSONA_RUNTIME_SOCKET"])
		}
		if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("HTTPS proxy not injected: %v", m)
		}
		if m["ALL_PROXY"] != "socks5h://127.0.0.1:7890" || m["all_proxy"] != "socks5h://127.0.0.1:7890" {
			t.Fatalf("ALL proxy not injected: %v", m)
		}
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("adds single-label gateway host to NO_PROXY when ExcludeSimpleHostnames is on", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy { return detected }
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "GOOGLE_GEMINI_BASE_URL=http://gateway"})
		m := envMap(got)
		wantNoProxy := "localhost,127.0.0.1,::1,192.0.2.0/24,.local,gateway"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("merges existing NO_PROXY with loopback and system exceptions", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy { return detected }
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "NO_PROXY=gateway.example.com"})
		m := envMap(got)
		wantNoProxy := "gateway.example.com,localhost,127.0.0.1,::1,192.0.2.0/24,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("explicit proxy suppresses system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return detected
		}
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "HTTPS_PROXY=http://proxy.example.com:3128"})
		if called {
			t.Fatal("detectSystemProxy should not be called when base explicitly sets HTTPS_PROXY")
		}
		m := envMap(got)
		if m["HTTPS_PROXY"] != "http://proxy.example.com:3128" {
			t.Fatalf("HTTPS_PROXY = %q", m["HTTPS_PROXY"])
		}
		if _, ok := m["ALL_PROXY"]; ok {
			t.Fatalf("unexpected ALL_PROXY injected alongside explicit proxy: %v", m)
		}
	})

	t.Run("explicit empty proxy disables system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return detected
		}
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "HTTP_PROXY="})
		if called {
			t.Fatal("detectSystemProxy should not be called when base explicitly clears HTTP_PROXY")
		}
		m := envMap(got)
		if v, ok := m["HTTP_PROXY"]; !ok || v != "" {
			t.Fatalf("HTTP_PROXY = %q (ok=%v), want explicit empty", v, ok)
		}
		if _, ok := m["HTTPS_PROXY"]; ok {
			t.Fatalf("unexpected HTTPS_PROXY injected: %v", m)
		}
	})

	t.Run("process explicit empty proxy disables system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return detected
		}
		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "HTTPS_PROXY="})
		if called {
			t.Fatal("detectSystemProxy should not be called when process env explicitly sets HTTPS_PROXY=")
		}
		m := envMap(got)
		if v, ok := m["HTTPS_PROXY"]; !ok || v != "" {
			t.Fatalf("HTTPS_PROXY = %q (ok=%v), want explicit empty", v, ok)
		}
		if _, ok := m["HTTP_PROXY"]; ok {
			t.Fatalf("unexpected HTTP_PROXY injected: %v", m)
		}
	})
}

func TestAgyBridgeCommandInjectsSystemProxyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	pkgRoot := filepath.Join(dir, "pkgs")
	companion := filepath.Join(pkgRoot, agyPackage, "antigravity", "companion.mjs")
	if err := os.MkdirAll(filepath.Dir(companion), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companion, []byte("// test companion\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimepkg.OverrideEnv, pkgRoot)

	fakeNode := filepath.Join(dir, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nprintf 22.14.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_NODEJS", fakeNode)

	oldDetect, oldShell := detectSystemProxy, detectShellProxy
	t.Cleanup(func() { detectSystemProxy, detectShellProxy = oldDetect, oldShell })
	detectShellProxy = func(context.Context) shellProxy {
		t.Error("detectShellProxy should not be called when explicit or system proxy applies")
		return shellProxy{}
	}

	allProxyAndBypass := append(slices.Clone(proxyVars), "NO_PROXY", "no_proxy")

	t.Run("inherits system proxy when proxy env vars are unset", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy {
			return systemProxy{
				HTTP:       "http://127.0.0.1:7890",
				HTTPS:      "http://127.0.0.1:7890",
				SOCKS:      "socks5h://127.0.0.1:7890",
				Exceptions: []string{".local"},
			}
		}
		unsetEnv(t, allProxyAndBypass...)

		rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
		cmd, err := agyBridgeCommand(context.Background(), rh, "/opt/bin/agy", "builtin-test")
		if err != nil {
			t.Fatalf("agyBridgeCommand: %v", err)
		}
		m := envMap(cmd.Env)
		if m["PERSONA_RUNTIME_SOCKET"] != rh.path {
			t.Fatalf("PERSONA_RUNTIME_SOCKET = %q, want %q", m["PERSONA_RUNTIME_SOCKET"], rh.path)
		}
		if m["HTTP_PROXY"] != "http://127.0.0.1:7890" || m["http_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("HTTP proxy missing from cmd.Env: HTTP_PROXY=%q http_proxy=%q", m["HTTP_PROXY"], m["http_proxy"])
		}
		if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("HTTPS proxy missing from cmd.Env: HTTPS_PROXY=%q https_proxy=%q", m["HTTPS_PROXY"], m["https_proxy"])
		}
		if m["ALL_PROXY"] != "socks5h://127.0.0.1:7890" || m["all_proxy"] != "socks5h://127.0.0.1:7890" {
			t.Fatalf("ALL proxy missing from cmd.Env: ALL_PROXY=%q all_proxy=%q", m["ALL_PROXY"], m["all_proxy"])
		}
		wantNoProxy := "localhost,127.0.0.1,::1,.local"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("explicit empty HTTPS_PROXY= in process env suppresses system proxy fallback", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return systemProxy{HTTP: "http://127.0.0.1:7890", HTTPS: "http://127.0.0.1:7890"}
		}
		unsetEnv(t, allProxyAndBypass...)
		t.Setenv("HTTPS_PROXY", "")

		rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
		cmd, err := agyBridgeCommand(context.Background(), rh, "/opt/bin/agy", "builtin-test")
		if err != nil {
			t.Fatalf("agyBridgeCommand: %v", err)
		}
		if called {
			t.Fatal("detectSystemProxy should not be called when HTTPS_PROXY= is explicitly set")
		}
		m := envMap(cmd.Env)
		if v, ok := m["HTTPS_PROXY"]; !ok || v != "" {
			t.Fatalf("HTTPS_PROXY = %q (ok=%v), want explicit empty", v, ok)
		}
		if _, ok := m["HTTP_PROXY"]; ok {
			t.Fatalf("unexpected HTTP_PROXY=%q when HTTPS_PROXY= was explicitly set", m["HTTP_PROXY"])
		}
	})

	t.Run("preserves explicit process proxy without probing system proxy", func(t *testing.T) {
		called := false
		detectSystemProxy = func(context.Context) systemProxy {
			called = true
			return systemProxy{HTTP: "http://127.0.0.1:7890", HTTPS: "http://127.0.0.1:7890"}
		}
		unsetEnv(t, allProxyAndBypass...)
		t.Setenv("HTTPS_PROXY", "http://proxy.example.com:8888")

		rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
		cmd, err := agyBridgeCommand(context.Background(), rh, "/opt/bin/agy", "builtin-test")
		if err != nil {
			t.Fatalf("agyBridgeCommand: %v", err)
		}
		if called {
			t.Fatal("detectSystemProxy should not be called when HTTPS_PROXY is explicitly set")
		}
		m := envMap(cmd.Env)
		if m["HTTPS_PROXY"] != "http://proxy.example.com:8888" {
			t.Fatalf("HTTPS_PROXY = %q, want http://proxy.example.com:8888", m["HTTPS_PROXY"])
		}
		if _, ok := m["HTTP_PROXY"]; ok {
			t.Fatalf("unexpected HTTP_PROXY=%q when explicit HTTPS_PROXY was set", m["HTTP_PROXY"])
		}
	})
}

func TestReadSystemProxyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := readSystemProxy(ctx); got.hasProxy() || len(got.Exceptions) != 0 {
		t.Fatalf("readSystemProxy(canceled) = %+v, want empty", got)
	}
}

func TestParseShellProxyOutput(t *testing.T) {
	frame := func(pairs ...string) string {
		return shellProxyBegin + strings.Join(pairs, "\x00") + shellProxyEnd
	}
	tests := []struct {
		name string
		out  string
		want shellProxy
	}{
		{
			name: "framed output ignores startup banner trailing trap and non-proxy secrets",
			out: "Welcome banner\n\x1b[32mhello\x1b[0m\n" +
				frame(
					"HTTP_PROXY=http://127.0.0.1:7890",
					"http_proxy=http://127.0.0.1:7890",
					"HTTPS_PROXY=http://127.0.0.1:7890",
					"https_proxy=http://127.0.0.1:7890",
					"ALL_PROXY=socks5://127.0.0.1:7891",
					"all_proxy=socks5://127.0.0.1:7891",
					"NO_PROXY=.corp.example.com,192.0.2.0/24",
					"no_proxy=localhost,127.0.0.1",
					"GEMINI_API_KEY=must-never-leak",
					"ANTHROPIC_API_KEY=must-never-leak",
				) + "\nexit trap noise\n",
			want: shellProxy{
				HTTPProxy:  "http://127.0.0.1:7890",
				httpProxy:  "http://127.0.0.1:7890",
				HTTPSProxy: "http://127.0.0.1:7890",
				httpsProxy: "http://127.0.0.1:7890",
				ALLProxy:   "socks5://127.0.0.1:7891",
				allProxy:   "socks5://127.0.0.1:7891",
				NoProxy:    ".corp.example.com,192.0.2.0/24",
				noProxy:    "localhost,127.0.0.1",
			},
		},
		{
			name: "lowercase only clash shell export",
			out: frame(
				"HTTP_PROXY=",
				"http_proxy=http://127.0.0.1:7890",
				"HTTPS_PROXY=",
				"https_proxy=http://127.0.0.1:7890",
				"ALL_PROXY=",
				"all_proxy=socks5://127.0.0.1:7890",
				"NO_PROXY=",
				"no_proxy=",
			),
			want: shellProxy{
				httpProxy:  "http://127.0.0.1:7890",
				httpsProxy: "http://127.0.0.1:7890",
				allProxy:   "socks5://127.0.0.1:7890",
			},
		},
		{
			name: "drops invalid proxy urls and poisoned no_proxy entries without dropping valid peers",
			out: frame(
				"HTTP_PROXY=http://127.0.0.1:0",
				"http_proxy=http://127.0.0.1:7890",
				"HTTPS_PROXY=file:///etc/passwd",
				"https_proxy=http://127.0.0.1:7890\r\nEVIL=1",
				"ALL_PROXY=socks5h://[::1]:1080",
				"all_proxy=$all_proxy",
				"NO_PROXY=.corp.example.com,*.local,bad\nEVIL=1,example.com/path,192.0.2/24,a;b,$(id),`id`,foo|bar,foo&bar,foo(1),localhost:8080",
				"no_proxy=*",
			),
			want: shellProxy{
				httpProxy: "http://127.0.0.1:7890",
				ALLProxy:  "socks5h://[::1]:1080",
				NoProxy:   ".corp.example.com,.local,192.0.2.0/24,localhost:8080",
				noProxy:   "*",
			},
		},
		{
			name: "missing end marker fails open to empty",
			out:  shellProxyBegin + "HTTPS_PROXY=http://127.0.0.1:7890",
			want: shellProxy{},
		},
		{
			name: "missing begin marker fails open to empty",
			out:  "HTTPS_PROXY=http://127.0.0.1:7890" + shellProxyEnd,
			want: shellProxy{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseShellProxyOutput(tc.out)
			if got != tc.want {
				t.Fatalf("parseShellProxyOutput() =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

func TestValidateShellProxyURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"http ipv4", "http://127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"https trailing slash", "https://proxy.example.com:8443/", "https://proxy.example.com:8443/"},
		{"uppercase scheme", "HTTP://127.0.0.1:7890", "HTTP://127.0.0.1:7890"},
		{"trimmed spaces and tabs", " \thttp://127.0.0.1:7890\t ", "http://127.0.0.1:7890"},
		{"socks5", "socks5://127.0.0.1:7890", "socks5://127.0.0.1:7890"},
		{"socks5h ipv6", "socks5h://[::1]:7891", "socks5h://[::1]:7891"},
		{"ipv6 default port", "http://[2001:db8::1]", "http://[2001:db8::1]"},
		{"ipv6 zone", "http://[fe80::1%25en0]:7890", "http://[fe80::1%25en0]:7890"},
		{"userinfo auth", "http://alice:secret@proxy.example.com:8080", "http://alice:secret@proxy.example.com:8080"},
		{"default port hostname", "http://proxy.example.com", "http://proxy.example.com"},
		{"bare ipv4 host port", "127.0.0.1:7890", "127.0.0.1:7890"},
		{"bare ipv6 host port", "[::1]:7890", "[::1]:7890"},
		{"empty", "", ""},
		{"bare word", "direct", ""},
		{"unexpanded shell var", "$http_proxy", ""},
		{"port zero", "http://127.0.0.1:0", ""},
		{"port overflow", "http://127.0.0.1:65536", ""},
		{"port leading zero", "http://127.0.0.1:07890", ""},
		{"empty trailing colon", "http://127.0.0.1:", ""},
		{"empty ipv6 trailing colon", "http://[::1]:", ""},
		{"missing host", "http://:7890", ""},
		{"unsupported file scheme", "file:///etc/passwd", ""},
		{"unsupported ftp scheme", "ftp://proxy.example.com:21", ""},
		{"path rejected", "http://127.0.0.1:7890/evil", ""},
		{"query rejected", "http://127.0.0.1:7890?a=1", ""},
		{"empty query rejected", "http://127.0.0.1:7890?", ""},
		{"fragment rejected", "http://127.0.0.1:7890#x", ""},
		{"empty fragment rejected", "http://127.0.0.1:7890#", ""},
		{"crlf rejected", "http://127.0.0.1:7890\r\nX=1", ""},
		{"trailing newline rejected", "http://127.0.0.1:7890\n", ""},
		{"nul rejected", "http://127.0.0.1:7890\x00", ""},
		{"bracketed ipv4 rejected", "http://[127.0.0.1]:7890", ""},
		{"bracketed hostname rejected", "http://[proxy.example.com]:7890", ""},
		{"bare userinfo rejected", "user@127.0.0.1:7890", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateShellProxyURL(tc.raw); got != tc.want {
				t.Fatalf("validateShellProxyURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestApplyShellProxyEnvSyncsCaseAndPreservesLoopbackNoProxy(t *testing.T) {
	t.Run("lowercase only syncs uppercase and injects loopback no_proxy", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		orig := slices.Clone(base)
		got := applyShellProxyEnv(base, shellProxy{
			httpProxy:  "http://127.0.0.1:7890",
			httpsProxy: "http://127.0.0.1:7890",
			allProxy:   "socks5://127.0.0.1:7891",
		})
		if !slices.Equal(base, orig) {
			t.Fatalf("input slice mutated: %v", base)
		}
		m := envMap(got)
		for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
			if m[k] != "http://127.0.0.1:7890" {
				t.Fatalf("%s = %q, want http://127.0.0.1:7890", k, m[k])
			}
			if envKeyCount(got, k) != 1 {
				t.Fatalf("%s appeared %d times in %v", k, envKeyCount(got, k), got)
			}
		}
		for _, k := range []string{"ALL_PROXY", "all_proxy"} {
			if m[k] != "socks5://127.0.0.1:7891" {
				t.Fatalf("%s = %q, want socks5://127.0.0.1:7891", k, m[k])
			}
		}
		if m["NO_PROXY"] != loopbackNoProxy || m["no_proxy"] != loopbackNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], loopbackNoProxy)
		}
	})

	t.Run("distinct upper and lower values stay distinct while merging base and shell NO_PROXY", func(t *testing.T) {
		base := []string{"PATH=/usr/bin", "NO_PROXY=gateway.example.com,127.0.0.1"}
		got := applyShellProxyEnv(base, shellProxy{
			HTTPSProxy: "http://upper.example.com:8080",
			httpsProxy: "http://lower.example.com:8080",
			NoProxy:    ".corp.example.com,LOCALHOST",
			noProxy:    "internal.example.org",
		})
		m := envMap(got)
		if m["HTTPS_PROXY"] != "http://upper.example.com:8080" || m["https_proxy"] != "http://lower.example.com:8080" {
			t.Fatalf("HTTPS_PROXY=%q https_proxy=%q", m["HTTPS_PROXY"], m["https_proxy"])
		}
		for _, k := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			if _, ok := m[k]; ok {
				t.Fatalf("unexpected %s=%q when shell set only HTTPS proxy", k, m[k])
			}
		}
		wantNoProxy := "gateway.example.com,127.0.0.1,localhost,::1,.corp.example.com,internal.example.org"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("no_proxy only or invalid urls leave env untouched", func(t *testing.T) {
		base := []string{"PATH=/usr/bin"}
		for _, p := range []shellProxy{
			{NoProxy: ".corp.example.com"},
			{HTTPSProxy: "file:///etc/passwd", NoProxy: ".corp.example.com"},
		} {
			if got := applyShellProxyEnv(base, p); !slices.Equal(got, base) {
				t.Fatalf("applyShellProxyEnv(%+v) modified env: got %v, want %v", p, got, base)
			}
		}
	})

	t.Run("preserves wildcard NO_PROXY from shell", func(t *testing.T) {
		got := applyShellProxyEnv([]string{"PATH=/usr/bin"}, shellProxy{
			HTTPSProxy: "http://127.0.0.1:7890",
			NoProxy:    "*",
		})
		m := envMap(got)
		if m["NO_PROXY"] != "*" || m["no_proxy"] != "*" {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want *", m["NO_PROXY"], m["no_proxy"])
		}
	})
}

func TestWithShellProxyEnvRespectsExplicitEnv(t *testing.T) {
	oldShell := detectShellProxy
	t.Cleanup(func() { detectShellProxy = oldShell })

	for _, key := range proxyVars {
		t.Run(key+" non-empty", func(t *testing.T) {
			called := false
			detectShellProxy = func(context.Context) shellProxy {
				called = true
				return shellProxy{HTTPSProxy: "http://127.0.0.1:7890"}
			}
			base := []string{"PATH=/usr/bin", key + "=http://proxy.example.com:8888"}
			got := withShellProxyEnv(context.Background(), base)
			if called {
				t.Fatalf("detectShellProxy was called despite explicit %s", key)
			}
			if !slices.Equal(got, base) {
				t.Fatalf("env changed despite explicit %s: got %v, want %v", key, got, base)
			}
		})

		t.Run(key+" explicit empty disables fallback", func(t *testing.T) {
			called := false
			detectShellProxy = func(context.Context) shellProxy {
				called = true
				return shellProxy{HTTPSProxy: "http://127.0.0.1:7890"}
			}
			base := []string{"PATH=/usr/bin", key + "="}
			got := withShellProxyEnv(context.Background(), base)
			if called {
				t.Fatalf("detectShellProxy was called despite explicit empty %s=", key)
			}
			if !slices.Equal(got, base) {
				t.Fatalf("env changed despite explicit empty %s=: got %v, want %v", key, got, base)
			}
		})
	}
}

func TestAgyBridgeEnvProxyPrecedence(t *testing.T) {
	oldSys, oldShell := detectSystemProxy, detectShellProxy
	t.Cleanup(func() {
		detectSystemProxy, detectShellProxy = oldSys, oldShell
	})

	sysProxy := systemProxy{HTTPS: "http://127.0.0.1:7890"}
	shProxy := shellProxy{
		httpProxy:  "http://127.0.0.1:9990",
		httpsProxy: "http://127.0.0.1:9990",
		allProxy:   "socks5://127.0.0.1:9991",
		NoProxy:    ".corp.example.com",
	}

	t.Run("explicit process env beats system and shell without probing either", func(t *testing.T) {
		sysCalled, shellCalled := false, false
		detectSystemProxy = func(context.Context) systemProxy { sysCalled = true; return sysProxy }
		detectShellProxy = func(context.Context) shellProxy { shellCalled = true; return shProxy }

		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "HTTPS_PROXY=http://explicit.example.com:3128"})
		if sysCalled || shellCalled {
			t.Fatalf("unexpected fallback probe: sys=%v shell=%v", sysCalled, shellCalled)
		}
		m := envMap(got)
		if m["HTTPS_PROXY"] != "http://explicit.example.com:3128" {
			t.Fatalf("HTTPS_PROXY = %q", m["HTTPS_PROXY"])
		}
		if _, ok := m["ALL_PROXY"]; ok {
			t.Fatalf("unexpected ALL_PROXY injected: %v", m)
		}
	})

	t.Run("explicit empty process proxy disables both system and shell fallback", func(t *testing.T) {
		sysCalled, shellCalled := false, false
		detectSystemProxy = func(context.Context) systemProxy { sysCalled = true; return sysProxy }
		detectShellProxy = func(context.Context) shellProxy { shellCalled = true; return shProxy }

		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "HTTPS_PROXY="})
		if sysCalled || shellCalled {
			t.Fatalf("unexpected fallback probe: sys=%v shell=%v", sysCalled, shellCalled)
		}
		m := envMap(got)
		if v, ok := m["HTTPS_PROXY"]; !ok || v != "" {
			t.Fatalf("HTTPS_PROXY = %q (ok=%v), want explicit empty", v, ok)
		}
	})

	t.Run("explicit proxy and explicit empty proxy disable both system and shell fallback", func(t *testing.T) {
		for _, kv := range []string{"HTTPS_PROXY=http://provider.example.com:3128", "HTTP_PROXY="} {
			sysCalled, shellCalled := false, false
			detectSystemProxy = func(context.Context) systemProxy { sysCalled = true; return sysProxy }
			detectShellProxy = func(context.Context) shellProxy { shellCalled = true; return shProxy }

			rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
			got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", kv})
			if sysCalled || shellCalled {
				t.Fatalf("base %q triggered fallback probe: sys=%v shell=%v", kv, sysCalled, shellCalled)
			}
			k, want, _ := strings.Cut(kv, "=")
			m := envMap(got)
			if v, ok := m[k]; !ok || v != want {
				t.Fatalf("base %q: %s = %q (ok=%v), want %q", kv, k, v, ok, want)
			}
			if _, ok := m["ALL_PROXY"]; ok {
				t.Fatalf("base %q leaked shell ALL_PROXY: %v", kv, m)
			}
		}
	})

	t.Run("system proxy beats login shell without probing login shell", func(t *testing.T) {
		sysCalled, shellCalled := false, false
		detectSystemProxy = func(context.Context) systemProxy { sysCalled = true; return sysProxy }
		detectShellProxy = func(context.Context) shellProxy { shellCalled = true; return shProxy }

		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		if !sysCalled {
			t.Fatal("detectSystemProxy was not called")
		}
		if shellCalled {
			t.Fatal("detectShellProxy should not be called when system proxy is active")
		}
		m := envMap(got)
		if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
			t.Fatalf("system proxy did not win: %v", m)
		}
		if _, ok := m["ALL_PROXY"]; ok {
			t.Fatalf("shell ALL_PROXY leaked despite system proxy: %v", m)
		}
	})

	t.Run("falls back to login shell when process and system proxy are absent", func(t *testing.T) {
		sysCalled, shellCalled := false, false
		detectSystemProxy = func(context.Context) systemProxy { sysCalled = true; return systemProxy{} }
		detectShellProxy = func(context.Context) shellProxy { shellCalled = true; return shProxy }

		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin", "NO_PROXY=gateway.example.com"})
		if !sysCalled || !shellCalled {
			t.Fatalf("expected both probes in order: sys=%v shell=%v", sysCalled, shellCalled)
		}
		m := envMap(got)
		if m["PERSONA_RUNTIME_SOCKET"] != "/tmp/persona-runtime.sock" {
			t.Fatalf("bridge socket env missing: %v", m)
		}
		if m["HTTP_PROXY"] != "http://127.0.0.1:9990" || m["http_proxy"] != "http://127.0.0.1:9990" {
			t.Fatalf("HTTP proxy missing: %v", m)
		}
		if m["HTTPS_PROXY"] != "http://127.0.0.1:9990" || m["https_proxy"] != "http://127.0.0.1:9990" {
			t.Fatalf("HTTPS proxy missing: %v", m)
		}
		if m["ALL_PROXY"] != "socks5://127.0.0.1:9991" || m["all_proxy"] != "socks5://127.0.0.1:9991" {
			t.Fatalf("ALL proxy missing: %v", m)
		}
		wantNoProxy := "gateway.example.com,localhost,127.0.0.1,::1,.corp.example.com"
		if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
			t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
		}
	})

	t.Run("no proxy in process system or shell leaves env untouched", func(t *testing.T) {
		detectSystemProxy = func(context.Context) systemProxy { return systemProxy{} }
		detectShellProxy = func(context.Context) shellProxy { return shellProxy{} }

		rh := NewRuntimeHost("test", "/tmp/persona-runtime.sock")
		got := agyBridgeEnv(context.Background(), rh, []string{"PATH=/usr/bin"})
		m := envMap(got)
		for _, k := range append(slices.Clone(proxyVars), "NO_PROXY", "no_proxy") {
			if _, ok := m[k]; ok {
				t.Fatalf("unexpected %s=%q when no proxy is configured anywhere", k, m[k])
			}
		}
	})
}

func TestAgyBridgeCommandFallsBackToShellProxyEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	pkgRoot := filepath.Join(dir, "pkgs")
	companion := filepath.Join(pkgRoot, agyPackage, "antigravity", "companion.mjs")
	if err := os.MkdirAll(filepath.Dir(companion), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companion, []byte("// test companion\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimepkg.OverrideEnv, pkgRoot)

	fakeNode := filepath.Join(dir, "node")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nprintf 22.14.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_NODEJS", fakeNode)

	oldSys, oldShell := detectSystemProxy, detectShellProxy
	t.Cleanup(func() { detectSystemProxy, detectShellProxy = oldSys, oldShell })
	detectSystemProxy = func(context.Context) systemProxy { return systemProxy{} }
	detectShellProxy = func(context.Context) shellProxy {
		return shellProxy{
			httpsProxy: "http://127.0.0.1:7890",
			NoProxy:    ".corp.local",
		}
	}

	unsetEnv(t, append(slices.Clone(proxyVars), "NO_PROXY", "no_proxy")...)
	rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
	cmd, err := agyBridgeCommand(context.Background(), rh, "/opt/bin/agy", "builtin-test")
	if err != nil {
		t.Fatalf("agyBridgeCommand: %v", err)
	}
	m := envMap(cmd.Env)
	if m["HTTPS_PROXY"] != "http://127.0.0.1:7890" || m["https_proxy"] != "http://127.0.0.1:7890" {
		t.Fatalf("shell HTTPS proxy missing from cmd.Env: HTTPS_PROXY=%q https_proxy=%q", m["HTTPS_PROXY"], m["https_proxy"])
	}
	wantNoProxy := "localhost,127.0.0.1,::1,.corp.local"
	if m["NO_PROXY"] != wantNoProxy || m["no_proxy"] != wantNoProxy {
		t.Fatalf("NO_PROXY=%q no_proxy=%q, want %q", m["NO_PROXY"], m["no_proxy"], wantNoProxy)
	}
}

func TestReadShellProxyRealShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell proxy probe is disabled on Windows")
	}

	t.Run("canceled context returns empty without spawning shell", func(t *testing.T) {
		oldCmd := shellProxyCommand
		t.Cleanup(func() { shellProxyCommand = oldCmd })
		spawned := false
		shellProxyCommand = func(ctx context.Context, script string) *exec.Cmd {
			spawned = true
			return loginShell(ctx, script)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := readShellProxy(ctx); got.hasProxy() || spawned {
			t.Fatalf("readShellProxy(canceled) = %+v spawned=%v, want empty and no spawn", got, spawned)
		}
	})

	t.Run("noisy stdout past max buffer still preserves trailing framed proxy", func(t *testing.T) {
		oldCmd := shellProxyCommand
		t.Cleanup(func() { shellProxyCommand = oldCmd })
		shellProxyCommand = func(ctx context.Context, script string) *exec.Cmd {
			// Emit >1 MiB of noise before running the probe script so tailBuffer wraps without dropping the frame.
			noisy := "dd if=/dev/zero bs=4096 count=320 2>/dev/null | tr '\\0' 'A'; " + script
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "export HTTPS_PROXY=http://127.0.0.1:7898; "+noisy)
			cmd.Stdin = nil
			return cmd
		}
		got := readShellProxy(context.Background())
		if got.HTTPSProxy != "http://127.0.0.1:7898" {
			t.Fatalf("readShellProxy(noisy) = %+v, want HTTPSProxy http://127.0.0.1:7898", got)
		}
	})

	t.Run("probe runs with cmd.Dir set to userHome", func(t *testing.T) {
		home := t.TempDir()
		realHome, err := filepath.EvalSymlinks(home)
		if err != nil {
			realHome = home
		}
		t.Setenv("HOME", home)
		oldCmd := shellProxyCommand
		t.Cleanup(func() { shellProxyCommand = oldCmd })
		pwdFile := filepath.Join(home, "seen-pwd")
		shellProxyCommand = func(ctx context.Context, _ string) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `pwd > "$HOME/seen-pwd"`)
			cmd.Stdin = nil
			return cmd
		}
		_ = readShellProxy(context.Background())
		raw, err := os.ReadFile(pwdFile)
		if err != nil {
			t.Fatalf("read pwdFile: %v", err)
		}
		gotDir := strings.TrimSpace(string(raw))
		if gotReal, err := filepath.EvalSymlinks(gotDir); err == nil {
			gotDir = gotReal
		}
		if gotDir != realHome {
			t.Fatalf("shell probe cwd = %q, want %q", gotDir, realHome)
		}
	})

	t.Run("hanging shell times out and fails open without blocking", func(t *testing.T) {
		oldCmd, oldTimeout := shellProxyCommand, shellProxyTimeout
		t.Cleanup(func() { shellProxyCommand, shellProxyTimeout = oldCmd, oldTimeout })
		shellProxyTimeout = 150 * time.Millisecond
		shellProxyCommand = func(ctx context.Context, _ string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30")
		}
		start := time.Now()
		got := readShellProxy(context.Background())
		elapsed := time.Since(start)
		if got.hasProxy() {
			t.Fatalf("hanging shell returned proxy %+v, want empty", got)
		}
		if elapsed > 3*time.Second {
			t.Fatalf("hanging shell took %v to time out, want bounded by shellProxyTimeout", elapsed)
		}
	})

	zshPath, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}

	t.Run("reads exported proxy vars from zshrc ignores banners functions unexported vars and parent env", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ZDOTDIR", home)
		t.Setenv("SHELL", zshPath)
		// Parent env proxy must be stripped before probing so only shell rc exports are observed.
		t.Setenv("HTTP_PROXY", "http://parent.example.com:9999")
		t.Setenv("NO_PROXY", "parent.example.com")

		sourced := filepath.Join(home, ".proxy_extra")
		if err := os.WriteFile(sourced, []byte("export all_proxy=socks5h://127.0.0.1:7891\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		zshrc := strings.Join([]string{
			`echo "startup banner noise"`,
			`local_http_proxy=http://127.0.0.1:1111`,
			`HTTP_PROXY=http://127.0.0.1:2222`, // unexported shell-local variable
			`proxy_on() { export HTTP_PROXY=http://127.0.0.1:3333; }`,
			`PROXY_BASE="http://127.0.0.1:7890"`,
			`export https_proxy="$PROXY_BASE"`,
			`export NO_PROXY=".corp.example.com"`,
			`export GEMINI_API_KEY="super-secret-value"`,
			`source "$HOME/.proxy_extra"`,
		}, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(zshrc), 0o600); err != nil {
			t.Fatal(err)
		}

		got := readShellProxy(context.Background())
		want := shellProxy{
			httpsProxy: "http://127.0.0.1:7890",
			allProxy:   "socks5h://127.0.0.1:7891",
			NoProxy:    ".corp.example.com",
		}
		if got != want {
			t.Fatalf("readShellProxy() =\n  %+v\nwant\n  %+v", got, want)
		}
	})

	t.Run("empty zshrc does not inherit parent process proxy", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("ZDOTDIR", home)
		t.Setenv("SHELL", zshPath)
		t.Setenv("HTTPS_PROXY", "http://parent.example.com:9999")
		if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("# no proxy here\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := readShellProxy(context.Background()); got != (shellProxy{}) {
			t.Fatalf("readShellProxy() = %+v, want empty", got)
		}
	})

	if runtime.GOOS == "darwin" {
		t.Run("darwin zsh reads zprofile in addition to zshrc", func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", home)
			t.Setenv("SHELL", zshPath)
			if err := os.WriteFile(filepath.Join(home, ".zprofile"), []byte("export HTTPS_PROXY=http://127.0.0.1:7895\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("# no proxy here\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got := readShellProxy(context.Background())
			if got.HTTPSProxy != "http://127.0.0.1:7895" {
				t.Fatalf("readShellProxy() = %+v, want HTTPSProxy from .zprofile", got)
			}
		})

		t.Run("launchd minimal env without SHELL still reads zshrc proxy", func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", home)
			unsetEnv(t, "SHELL")
			t.Setenv("PATH", "/usr/bin:/bin:/usr/sbin:/sbin")
			if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export https_proxy=http://127.0.0.1:7896 http_proxy=http://127.0.0.1:7896\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got := readShellProxy(context.Background())
			if got.httpsProxy != "http://127.0.0.1:7896" || got.httpProxy != "http://127.0.0.1:7896" {
				t.Fatalf("readShellProxy(launchd env) = %+v, want http/https proxy 7896", got)
			}
		})
	}

	if bashPath, err := exec.LookPath("bash"); err == nil {
		t.Run("bash login shell without zshrc reads exported proxy from bashrc", func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			unsetEnv(t, "ZDOTDIR", "ENV", "BASH_ENV")
			t.Setenv("SHELL", bashPath)
			if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("export HTTP_PROXY=http://127.0.0.1:7897\nexport NO_PROXY=.corp.example.com\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			got := readShellProxy(context.Background())
			want := shellProxy{
				HTTPProxy: "http://127.0.0.1:7897",
				NoProxy:   ".corp.example.com",
			}
			if got != want {
				t.Fatalf("readShellProxy(bash) =\n  %+v\nwant\n  %+v", got, want)
			}
		})
	}
}

func TestShellProxyTailBuffer(t *testing.T) {
	b := &shellProxyTailBuffer{max: 10}
	_, _ = b.Write([]byte("abc"))
	if got := b.String(); got != "abc" {
		t.Fatalf("after 3B: got %q, want abc", got)
	}
	_, _ = b.Write([]byte("012345678"))
	if got := b.String(); got != "c012345678" {
		t.Fatalf("after wrap transition: got %q, want c012345678", got)
	}
	_, _ = b.Write([]byte("XYZ"))
	if got := b.String(); got != "2345678XYZ" {
		t.Fatalf("after ring wrap: got %q, want 2345678XYZ", got)
	}
	_, _ = b.Write([]byte("0123456789ABCDEF"))
	if got := b.String(); got != "6789ABCDEF" {
		t.Fatalf("after >=max write: got %q, want 6789ABCDEF", got)
	}
}

func TestSanitizeShellNoProxyAllowlist(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "keeps valid hosts suffixes ips cidrs ports and normalizes wildcard prefix",
			raw:  "localhost, 127.0.0.1, [::1], [::1]:8080, gateway, corp.example.com:8443, *.local, .corp.example.com, 192.0.2/24, 2001:db8::/32",
			want: "localhost,127.0.0.1,::1,[::1]:8080,gateway,corp.example.com:8443,.local,.corp.example.com,192.0.2.0/24,2001:db8::/32",
		},
		{
			name: "wildcard returns star",
			raw:  "localhost,*",
			want: "*",
		},
		{
			name: "rejects shell metacharacters controls zones and malformed tokens",
			raw:  "$(whoami),`id`,foo|bar,foo&bar,foo(bar),foo$bar,foo!bar,foo{bar},foo~bar,foo;bar,foo*bar,<local>,fe80::1%en0,example.com/path,bad\nEVIL=1,ok.example.com",
			want: "ok.example.com",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeShellNoProxy(tc.raw); got != tc.want {
				t.Fatalf("sanitizeShellNoProxy(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
