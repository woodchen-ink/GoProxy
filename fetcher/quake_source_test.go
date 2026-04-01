package fetcher

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"goproxy/config"
)

func TestFetchFromQuake(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if token := r.Header.Get("X-QuakeToken"); token != "test-token" {
			t.Fatalf("token = %q, want %q", token, "test-token")
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code": 0,
			"message": "Successful.",
			"data": [
				{"ip": "1.1.1.1", "port": 1080},
				{"ip": "1.1.1.1", "port": 1080},
				{"ip": "2.2.2.2", "port": 2080}
			]
		}`))
	}))
	defer server.Close()

	fetcher := &Fetcher{
		cfg: &config.Config{
			QuakeToken: "test-token",
		},
		client: &http.Client{
			Timeout: 2 * time.Second,
		},
	}

	proxies, err := fetcher.fetchFromQuake(Source{
		URL:      server.URL,
		Protocol: "socks5",
		Query:    `service:socks5 AND response:"No authentication"`,
		Limit:    10,
		Type:     sourceTypeQuake,
	})
	if err != nil {
		t.Fatalf("fetchFromQuake error: %v", err)
	}

	if len(proxies) != 2 {
		t.Fatalf("expected 2 proxies, got %d", len(proxies))
	}

	if proxies[0].Address != "1.1.1.1:1080" || proxies[1].Address != "2.2.2.2:2080" {
		t.Fatalf("unexpected proxies: %#v", proxies)
	}
}

func TestNormalizeQuakeResultSize(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{name: "default on zero", input: 0, want: defaultQuakeResultMax},
		{name: "default on negative", input: -1, want: defaultQuakeResultMax},
		{name: "clamp max", input: 20000, want: defaultQuakeResultMax},
		{name: "keep valid", input: 5000, want: 5000},
	}

	for _, tc := range tests {
		if got := normalizeQuakeResultSize(tc.input); got != tc.want {
			t.Fatalf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
