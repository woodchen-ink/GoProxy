package fetcher

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"goproxy/config"
	"goproxy/storage"
)

var htmlProxyURLRegex = regexp.MustCompile(`(?i)(socks5)://((?:\d{1,3}\.){3}\d{1,3}:\d{1,5})`)

type Fetcher struct {
	cfg           *config.Config
	fastSources   []Source
	slowSources   []Source
	sources       []Source
	client        *http.Client
	sourceManager *SourceManager
}

func New(cfg *config.Config, sourceManager *SourceManager) *Fetcher {
	fastSources, slowSources, allSources := buildSourceCatalog(cfg)

	return &Fetcher{
		cfg:           cfg,
		fastSources:   fastSources,
		slowSources:   slowSources,
		sources:       allSources,
		sourceManager: sourceManager,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// FetchSmart 智能抓取：根据模式和协议需求选择源
func (f *Fetcher) FetchSmart(mode string, preferredProtocol string) ([]storage.Proxy, error) {
	var sources []Source

	switch mode {
	case "emergency":
		// 紧急模式：忽略断路器，强制使用所有源（包括被禁用的）
		sources = f.filterAvailableSources(f.sources, preferredProtocol, true)
		log.Printf("[fetch] 🚨 紧急模式: 使用 %d 个源（忽略断路器）", len(sources))

	case "refill":
		// 补充模式：使用快更新源
		sources = f.filterAvailableSources(f.fastSources, preferredProtocol, false)
		log.Printf("[fetch] 🔄 补充模式: 使用 %d 个快更新源", len(sources))

	case "optimize":
		// 优化模式：随机选择2-3个慢更新源
		sources = f.selectRandomSources(f.slowSources, 3, preferredProtocol)
		log.Printf("[fetch] ⚡ 优化模式: 使用 %d 个源", len(sources))

	default:
		sources = f.filterAvailableSources(f.fastSources, preferredProtocol, false)
	}

	if len(sources) == 0 {
		return nil, fmt.Errorf("no available sources")
	}

	return f.fetchFromSources(sources)
}

// filterAvailableSources 过滤可用的源（通过断路器）
// ignoreCircuitBreaker: 是否忽略断路器（Emergency 模式下使用）
func (f *Fetcher) filterAvailableSources(sources []Source, preferredProtocol string, ignoreCircuitBreaker bool) []Source {
	var available []Source
	for _, src := range sources {
		// 检查断路器（紧急模式下忽略）
		if !ignoreCircuitBreaker && f.sourceManager != nil && !f.sourceManager.CanUseSource(src.statusKey()) {
			continue
		}
		// 如果指定了协议偏好，优先该协议的源
		if preferredProtocol != "" && src.Protocol != "" && src.Protocol != preferredProtocol {
			continue
		}
		available = append(available, src)
	}
	return available
}

// selectRandomSources 随机选择N个源
func (f *Fetcher) selectRandomSources(sources []Source, count int, preferredProtocol string) []Source {
	available := f.filterAvailableSources(sources, preferredProtocol, false)
	if len(available) <= count {
		return available
	}

	// 随机打乱
	shuffled := make([]Source, len(available))
	copy(shuffled, available)
	for i := range shuffled {
		j := i + int(time.Now().UnixNano())%(len(shuffled)-i)
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}

	return shuffled[:count]
}

// fetchFromSources 从指定源列表抓取
func (f *Fetcher) fetchFromSources(sources []Source) ([]storage.Proxy, error) {
	type result struct {
		proxies []storage.Proxy
		source  Source
		err     error
	}

	ch := make(chan result, len(sources))
	for _, src := range sources {
		go func(s Source) {
			proxies, err := f.fetchFromSource(s)
			ch <- result{proxies: proxies, source: s, err: err}
		}(src)
	}

	var all []storage.Proxy
	seen := make(map[string]bool)
	for range sources {
		r := <-ch
		if r.err != nil {
			log.Printf("[fetch] ❌ %s error: %v", r.source.label(), r.err)
			f.recordSourceFail(r.source)
			continue
		}

		// 记录成功
		f.recordSourceSuccess(r.source)

		// 去重
		var deduped []storage.Proxy
		for _, p := range r.proxies {
			if !seen[p.Address] {
				seen[p.Address] = true
				deduped = append(deduped, p)
			}
		}
		log.Printf("[fetch] ✅ %d 个 %s 代理 from %s", len(deduped), r.source.Protocol, r.source.label())
		all = append(all, deduped...)
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("no proxies fetched")
	}
	log.Printf("[fetch] 总共抓取: %d 个代理（去重后）", len(all))
	return all, nil
}

// Fetch 从所有来源并发抓取代理
func (f *Fetcher) Fetch() ([]storage.Proxy, error) {
	type result struct {
		proxies []storage.Proxy
		source  Source
		err     error
	}

	ch := make(chan result, len(f.sources))
	for _, src := range f.sources {
		go func(s Source) {
			proxies, err := f.fetchFromSource(s)
			ch <- result{proxies: proxies, source: s, err: err}
		}(src)
	}

	var all []storage.Proxy
	seen := make(map[string]bool)
	for range f.sources {
		r := <-ch
		if r.err != nil {
			log.Printf("fetch %s error: %v", r.source.label(), r.err)
			continue
		}
		// 去重
		var deduped []storage.Proxy
		for _, p := range r.proxies {
			if !seen[p.Address] {
				seen[p.Address] = true
				deduped = append(deduped, p)
			}
		}
		log.Printf("fetched %d %s proxies from %s", len(deduped), r.source.Protocol, r.source.label())
		all = append(all, deduped...)
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("no proxies fetched")
	}
	log.Printf("total fetched: %d proxies (deduped)", len(all))
	return all, nil
}

func (f *Fetcher) fetchFromSource(src Source) ([]storage.Proxy, error) {
	switch src.Type {
	case sourceTypeQuake:
		return f.fetchFromQuake(src)
	default:
		return f.fetchFromURL(src.URL, src.Protocol, src.Type)
	}
}

func (f *Fetcher) fetchFromURL(url, protocol string, sourceType SourceType) ([]storage.Proxy, error) {
	resp, err := f.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	if sourceType == sourceTypeHTML || strings.Contains(url, "socks5-proxy.github.io") {
		return parseHTMLProxyList(resp.Body, protocol)
	}

	return parseProxyList(resp.Body, protocol)
}

func (f *Fetcher) recordSourceSuccess(src Source) {
	if f.sourceManager == nil {
		return
	}
	f.sourceManager.RecordSuccess(src.statusKey())
}

func (f *Fetcher) recordSourceFail(src Source) {
	if f.sourceManager == nil {
		return
	}

	failThreshold := 3
	disableThreshold := 5
	cooldownMinutes := 30
	if f.cfg != nil {
		if f.cfg.SourceFailThreshold > 0 {
			failThreshold = f.cfg.SourceFailThreshold
		}
		if f.cfg.SourceDisableThreshold > 0 {
			disableThreshold = f.cfg.SourceDisableThreshold
		}
		if f.cfg.SourceCooldownMinutes > 0 {
			cooldownMinutes = f.cfg.SourceCooldownMinutes
		}
	}

	f.sourceManager.RecordFail(src.statusKey(), failThreshold, disableThreshold, cooldownMinutes)
}

// parseHTMLProxyList extracts embedded proxy URLs from HTML sources.
func parseHTMLProxyList(r io.Reader, protocol string) ([]storage.Proxy, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	matches := htmlProxyURLRegex.FindAllStringSubmatch(string(body), -1)
	seen := make(map[string]bool)
	proxies := make([]storage.Proxy, 0, len(matches))

	for _, match := range matches {
		proto := strings.ToLower(strings.TrimSpace(match[1]))
		if proto == "" {
			proto = protocol
		}

		addr := strings.TrimSpace(match[2])
		key := proto + "://" + addr
		if seen[key] {
			continue
		}
		seen[key] = true

		proxies = append(proxies, storage.Proxy{
			Address:  addr,
			Protocol: proto,
		})
	}

	return proxies, nil
}

// parseProxyList parses plain-text proxy lists with one entry per line.
func parseProxyList(r io.Reader, protocol string) ([]storage.Proxy, error) {
	var proxies []storage.Proxy
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		addr := line
		proto := protocol
		// 支持 protocol://host:port 格式
		if idx := strings.Index(line, "://"); idx != -1 {
			proto = line[:idx]
			addr = line[idx+3:]
			// socks4 当 socks5 处理
			if proto == "socks4" {
				proto = "socks5"
			}
		}
		parts := strings.Split(addr, ":")
		if len(parts) != 2 {
			continue
		}
		proxies = append(proxies, storage.Proxy{
			Address:  addr,
			Protocol: proto,
		})
	}
	return proxies, scanner.Err()
}
