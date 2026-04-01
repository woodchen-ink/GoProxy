package fetcher

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"goproxy/config"
	"goproxy/storage"
)

const (
	quakeSearchURL        = "https://quake.360.net/api/v3/search/quake_service"
	defaultQuakeResultMax = 10000
)

type quakeSearchRequest struct {
	Query   string   `json:"query"`
	Start   int      `json:"start"`
	Size    int      `json:"size"`
	Include []string `json:"include"`
}

type quakeSearchResponse struct {
	Code    any             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	Meta    struct {
		Pagination struct {
			Count int `json:"count"`
			Total int `json:"total"`
		} `json:"pagination"`
	} `json:"meta"`
}

// newQuakeSource 根据环境变量配置生成可选 Quake 抓取源。
func newQuakeSource(cfg *config.Config) (Source, bool) {
	if cfg == nil || !cfg.QuakeEnabled {
		return Source{}, false
	}

	token := strings.TrimSpace(cfg.QuakeToken)
	query := strings.TrimSpace(cfg.QuakeQuery)
	if token == "" || query == "" {
		return Source{}, false
	}

	return Source{
		Key:      "quake://quake_service",
		URL:      quakeSearchURL,
		Protocol: "socks5",
		Type:     sourceTypeQuake,
		Query:    query,
		Limit:    normalizeQuakeResultSize(cfg.QuakeResultSize),
	}, true
}

// fetchFromQuake 调用 Quake API 并抽取匿名 SOCKS5 的 IP:PORT 列表。
func (f *Fetcher) fetchFromQuake(src Source) ([]storage.Proxy, error) {
	if f.cfg == nil {
		return nil, fmt.Errorf("quake source requires config")
	}

	token := strings.TrimSpace(f.cfg.QuakeToken)
	if token == "" {
		return nil, fmt.Errorf("quake token is empty")
	}

	reqBody, err := json.Marshal(quakeSearchRequest{
		Query:   strings.TrimSpace(src.Query),
		Start:   0,
		Size:    normalizeQuakeResultSize(src.Limit),
		Include: []string{"ip", "port"},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal quake request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, src.URL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("build quake request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-QuakeToken", token)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request quake search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d from quake", resp.StatusCode)
	}

	var payload quakeSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode quake response: %w", err)
	}

	code := quakeResponseCode(payload.Code)
	if code != "0" {
		return nil, &sourceFetchError{
			err:           fmt.Errorf("quake api error [%s]: %s", code, strings.TrimSpace(payload.Message)),
			recordFailure: shouldRecordQuakeFailure(code),
		}
	}

	var rows []struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if len(payload.Data) == 0 || string(payload.Data) == "null" {
		return nil, nil
	}
	if err := json.Unmarshal(payload.Data, &rows); err != nil {
		return nil, fmt.Errorf("decode quake data: %w", err)
	}

	proxies := make([]storage.Proxy, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, item := range rows {
		if item.IP == "" || item.Port <= 0 || item.Port > 65535 {
			continue
		}

		address := fmt.Sprintf("%s:%d", strings.TrimSpace(item.IP), item.Port)
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}

		proxies = append(proxies, storage.Proxy{
			Address:  address,
			Protocol: src.Protocol,
		})
	}

	return proxies, nil
}

func shouldRecordQuakeFailure(code string) bool {
	switch code {
	case "q3005":
		// 调用过快属于账号配额/频控问题，不代表源本身失效。
		return false
	default:
		return true
	}
}

func normalizeQuakeResultSize(size int) int {
	switch {
	case size <= 0:
		return defaultQuakeResultMax
	case size > defaultQuakeResultMax:
		return defaultQuakeResultMax
	default:
		return size
	}
}

func quakeResponseCode(code any) string {
	switch value := code.(type) {
	case string:
		return value
	case float64:
		return fmt.Sprintf("%.0f", value)
	case int:
		return fmt.Sprintf("%d", value)
	case int64:
		return fmt.Sprintf("%d", value)
	default:
		return fmt.Sprintf("%v", value)
	}
}
