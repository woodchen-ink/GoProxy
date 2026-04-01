package fetcher

import (
	"strings"

	"goproxy/config"
)

type SourceType string

const (
	sourceTypePlainText SourceType = "plain_text"
	sourceTypeHTML      SourceType = "html"
	sourceTypeQuake     SourceType = "quake"
)

// Source 描述一个代理抓取源；静态源使用 URL，动态源可附带查询参数。
type Source struct {
	Key      string
	URL      string
	Protocol string // http 或 socks5
	Type     SourceType
	Query    string
	Limit    int
}

// 快速更新源（5-30分钟更新）- 用于紧急和补充模式。
var defaultFastUpdateSources = []Source{
	// proxifly - 每 5 分钟更新
	{URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/http/data.txt", Protocol: "http", Type: sourceTypePlainText},
	{URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/socks4/data.txt", Protocol: "socks5", Type: sourceTypePlainText},
	{URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/socks5/data.txt", Protocol: "socks5", Type: sourceTypePlainText},
	// socks5-proxy.github.io - HTML 页面内嵌 socks5:// 地址
	{URL: "https://socks5-proxy.github.io/", Protocol: "socks5", Type: sourceTypeHTML},
	// ProxyScraper - 每 30 分钟更新
	{URL: "https://raw.githubusercontent.com/ProxyScraper/ProxyScraper/main/http.txt", Protocol: "http", Type: sourceTypePlainText},
	{URL: "https://raw.githubusercontent.com/ProxyScraper/ProxyScraper/main/socks4.txt", Protocol: "socks5", Type: sourceTypePlainText},
	{URL: "https://raw.githubusercontent.com/ProxyScraper/ProxyScraper/main/socks5.txt", Protocol: "socks5", Type: sourceTypePlainText},
	// monosans - 每小时更新
	{URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt", Protocol: "http", Type: sourceTypePlainText},
}

// 慢速更新源（每天更新）- 用于优化轮换模式。
var defaultSlowUpdateSources = []Source{
	// TheSpeedX - 每天更新
	{URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt", Protocol: "http", Type: sourceTypePlainText},
	{URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks4.txt", Protocol: "socks5", Type: sourceTypePlainText},
	{URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks5.txt", Protocol: "socks5", Type: sourceTypePlainText},
	// monosans SOCKS
	{URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks4.txt", Protocol: "socks5", Type: sourceTypePlainText},
	{URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt", Protocol: "socks5", Type: sourceTypePlainText},
	// databay-labs - 备用源
	{URL: "https://cdn.jsdelivr.net/gh/databay-labs/free-proxy-list/http.txt", Protocol: "http", Type: sourceTypePlainText},
	{URL: "https://cdn.jsdelivr.net/gh/databay-labs/free-proxy-list/socks5.txt", Protocol: "socks5", Type: sourceTypePlainText},
}

// buildSourceCatalog 组合默认源和可选 API 源，便于主抓取流程保持简单。
func buildSourceCatalog(cfg *config.Config) (fast []Source, slow []Source, all []Source) {
	fast = cloneSources(defaultFastUpdateSources)
	slow = cloneSources(defaultSlowUpdateSources)

	if quakeSource, ok := newQuakeSource(cfg); ok {
		fast = append([]Source{quakeSource}, fast...)
	}

	all = append(cloneSources(fast), slow...)
	return fast, slow, all
}

func (s Source) statusKey() string {
	if key := strings.TrimSpace(s.Key); key != "" {
		return key
	}
	return s.URL
}

func (s Source) label() string {
	if key := strings.TrimSpace(s.Key); key != "" {
		return key
	}
	return s.URL
}

func cloneSources(sources []Source) []Source {
	cloned := make([]Source, len(sources))
	copy(cloned, sources)
	return cloned
}
