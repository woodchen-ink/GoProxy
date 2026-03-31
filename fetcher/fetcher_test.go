package fetcher

import (
	"strings"
	"testing"
)

// TestParseHTMLProxyList extracts SOCKS entries embedded inside HTML.
func TestParseHTMLProxyList(t *testing.T) {
	html := `
	<html>
		<body>
			<button onclick="copyProxy(this, 'socks5://8.8.8.8:1080 [2026-03-31 23:41]')">copy</button>
			<button onclick="copyProxy(this, 'socks5://8.8.8.8:1080 [2026-03-31 23:41]')">copy</button>
			<button onclick="copyProxy(this, 'socks4://1.1.1.1:8080 [2026-03-31 23:41]')">copy</button>
		</body>
	</html>`

	proxies, err := parseHTMLProxyList(strings.NewReader(html), "socks5")
	if err != nil {
		t.Fatalf("parseHTMLProxyList error: %v", err)
	}

	if len(proxies) != 1 {
		t.Fatalf("expected 1 proxy, got %d", len(proxies))
	}

	if proxies[0].Address != "8.8.8.8:1080" || proxies[0].Protocol != "socks5" {
		t.Fatalf("unexpected first proxy: %#v", proxies[0])
	}
}
