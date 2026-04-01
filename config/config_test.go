package config

import (
	"os"
	"path/filepath"
	"testing"
)

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

// TestResolveDataDir 保证容器部署和本地开发的数据目录选择稳定。
func TestResolveDataDir(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		want := filepath.Join(t.TempDir(), "custom-data")
		if got := resolveDataDir("  "+want+"  ", true); got != want {
			t.Fatalf("resolveDataDir(env) = %q, want %q", got, want)
		}
	})

	t.Run("container path fallback", func(t *testing.T) {
		if got := resolveDataDir("", true); got != defaultContainerDataDir {
			t.Fatalf("resolveDataDir(container) = %q, want %q", got, defaultContainerDataDir)
		}
	})

	t.Run("local cwd fallback", func(t *testing.T) {
		tempDir := t.TempDir()
		oldWD, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd: %v", err)
		}
		if err := os.Chdir(tempDir); err != nil {
			t.Fatalf("chdir temp: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chdir(oldWD); err != nil {
				t.Fatalf("restore wd: %v", err)
			}
		})

		want := filepath.Join(tempDir, "data")
		if got := resolveDataDir("", false); got != want {
			t.Fatalf("resolveDataDir(local) = %q, want %q", got, want)
		}
	})
}

// TestDataDirCreatesDirectory 保证首次启动时数据目录会自动创建。
func TestDataDirCreatesDirectory(t *testing.T) {
	customDir := filepath.Join(t.TempDir(), "runtime-data")
	t.Setenv("DATA_DIR", customDir)

	got := dataDir()
	if got != customDir {
		t.Fatalf("dataDir() = %q, want %q", got, customDir)
	}
	if !dirExists(customDir) {
		t.Fatalf("dataDir() did not create %q", customDir)
	}
}

// TestNormalizeListenAddr 保证环境变量里的端口写法会统一成监听地址。
func TestNormalizeListenAddr(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		fallback string
		want     string
	}{
		{name: "empty uses fallback", input: "", fallback: ":7777", want: ":7777"},
		{name: "plain port", input: "7777", fallback: ":7776", want: ":7777"},
		{name: "full addr", input: "0.0.0.0:7777", fallback: ":7776", want: "0.0.0.0:7777"},
		{name: "trim spaces", input: "  :7788  ", fallback: ":7776", want: ":7788"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeListenAddr(tc.input, tc.fallback); got != tc.want {
				t.Fatalf("normalizeListenAddr(%q, %q) = %q, want %q", tc.input, tc.fallback, got, tc.want)
			}
		})
	}
}

// TestDefaultConfigForcesMixedProxyPorts 保证 HTTP 和 SOCKS5 始终复用随机/最低两个端口。
func TestDefaultConfigForcesMixedProxyPorts(t *testing.T) {
	t.Setenv("RANDOM_PORT", "9001")
	t.Setenv("STABLE_PORT", "127.0.0.1:9002")
	t.Setenv("SOCKS5_RANDOM_PORT", "9101")
	t.Setenv("SOCKS5_STABLE_PORT", "127.0.0.1:9102")

	cfg := DefaultConfig()

	if cfg.ProxyPort != ":9001" {
		t.Fatalf("ProxyPort = %q, want %q", cfg.ProxyPort, ":9001")
	}
	if cfg.StableProxyPort != "127.0.0.1:9002" {
		t.Fatalf("StableProxyPort = %q, want %q", cfg.StableProxyPort, "127.0.0.1:9002")
	}
	if cfg.SOCKS5Port != cfg.ProxyPort {
		t.Fatalf("SOCKS5Port = %q, want same as ProxyPort %q", cfg.SOCKS5Port, cfg.ProxyPort)
	}
	if cfg.StableSOCKS5Port != cfg.StableProxyPort {
		t.Fatalf("StableSOCKS5Port = %q, want same as StableProxyPort %q", cfg.StableSOCKS5Port, cfg.StableProxyPort)
	}
}
