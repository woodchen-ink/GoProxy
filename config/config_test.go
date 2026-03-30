package config

import "testing"

// TestNormalizeSOCKS5DNSMode 保证三态模式和非法值回退行为稳定。
func TestNormalizeSOCKS5DNSMode(t *testing.T) {
	testCases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "remote", input: "remote", want: SOCKS5DNSModeRemote},
		{name: "fallback upper", input: "FALLBACK", want: SOCKS5DNSModeFallback},
		{name: "local spaced", input: " local ", want: SOCKS5DNSModeLocal},
		{name: "invalid", input: "invalid", want: SOCKS5DNSModeFallback},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeSOCKS5DNSMode(tc.input); got != tc.want {
				t.Fatalf("NormalizeSOCKS5DNSMode(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestSocks5DNSModeFromEnv 保证新旧环境变量的优先级与兼容映射正确。
func TestSocks5DNSModeFromEnv(t *testing.T) {
	t.Run("default fallback", func(t *testing.T) {
		t.Setenv("SOCKS5_DNS_MODE", "")
		t.Setenv("SOCKS5_LOCAL_DNS_FALLBACK", "")
		if got := socks5DNSModeFromEnv(); got != SOCKS5DNSModeFallback {
			t.Fatalf("default mode = %q, want %q", got, SOCKS5DNSModeFallback)
		}
	})

	t.Run("new env wins", func(t *testing.T) {
		t.Setenv("SOCKS5_DNS_MODE", "local")
		t.Setenv("SOCKS5_LOCAL_DNS_FALLBACK", "false")
		if got := socks5DNSModeFromEnv(); got != SOCKS5DNSModeLocal {
			t.Fatalf("mode = %q, want %q", got, SOCKS5DNSModeLocal)
		}
	})

	t.Run("legacy true maps to fallback", func(t *testing.T) {
		t.Setenv("SOCKS5_DNS_MODE", "")
		t.Setenv("SOCKS5_LOCAL_DNS_FALLBACK", "true")
		if got := socks5DNSModeFromEnv(); got != SOCKS5DNSModeFallback {
			t.Fatalf("mode = %q, want %q", got, SOCKS5DNSModeFallback)
		}
	})

	t.Run("legacy false maps to remote", func(t *testing.T) {
		t.Setenv("SOCKS5_DNS_MODE", "")
		t.Setenv("SOCKS5_LOCAL_DNS_FALLBACK", "false")
		if got := socks5DNSModeFromEnv(); got != SOCKS5DNSModeRemote {
			t.Fatalf("mode = %q, want %q", got, SOCKS5DNSModeRemote)
		}
	})
}
