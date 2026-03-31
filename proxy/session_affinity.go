package proxy

import (
	"strings"
	"sync"
	"time"

	"goproxy/storage"
)

const defaultSessionAffinityTTL = 10 * time.Minute

type affinityEntry struct {
	address   string
	expiresAt time.Time
}

type sessionAffinityManager struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]affinityEntry
}

func newSessionAffinityManager(ttl time.Duration) *sessionAffinityManager {
	if ttl <= 0 {
		ttl = defaultSessionAffinityTTL
	}
	return &sessionAffinityManager{
		ttl:     ttl,
		entries: make(map[string]affinityEntry),
	}
}

func (m *sessionAffinityManager) resolve(
	sessionID string,
	excludes []string,
	loader func(string) (*storage.Proxy, error),
	selector func([]string) (*storage.Proxy, error),
) (*storage.Proxy, error) {
	if sessionID == "" {
		return selector(excludes)
	}

	if stickyAddress, ok := m.lookup(sessionID, excludes); ok {
		if proxy, err := loader(stickyAddress); err == nil {
			m.bind(sessionID, stickyAddress)
			return proxy, nil
		}
		m.delete(sessionID)
	}

	proxy, err := selector(excludes)
	if err != nil {
		return nil, err
	}
	m.bind(sessionID, proxy.Address)
	return proxy, nil
}

func (m *sessionAffinityManager) lookup(sessionID string, excludes []string) (string, bool) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, entry := range m.entries {
		if now.After(entry.expiresAt) {
			delete(m.entries, key)
		}
	}

	entry, ok := m.entries[sessionID]
	if !ok {
		return "", false
	}
	for _, excluded := range excludes {
		if excluded == entry.address {
			return "", false
		}
	}
	return entry.address, true
}

func (m *sessionAffinityManager) bind(sessionID, address string) {
	if sessionID == "" || address == "" {
		return
	}
	m.mu.Lock()
	m.entries[sessionID] = affinityEntry{
		address:   address,
		expiresAt: time.Now().Add(m.ttl),
	}
	m.mu.Unlock()
}

func (m *sessionAffinityManager) delete(sessionID string) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	delete(m.entries, sessionID)
	m.mu.Unlock()
}

func parseSessionUsername(baseUsername, username string) (bool, string) {
	base := strings.TrimSpace(baseUsername)
	raw := strings.TrimSpace(username)
	if base == "" || raw == "" {
		return false, ""
	}
	if raw == base {
		return true, ""
	}

	prefixes := []string{
		base + "-session-",
		base + "__session__",
		base + ":session:",
		base + "-sid-",
		base + "__sid__",
		base + ":sid:",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(raw, prefix) {
			sessionID := strings.TrimSpace(raw[len(prefix):])
			if sessionID == "" {
				return false, ""
			}
			return true, sessionID
		}
	}
	return false, ""
}
