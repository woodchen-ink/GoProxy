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
	exitIP    string
	expiresAt time.Time
}

type sessionAffinityManager struct {
	mu           sync.Mutex
	ttl          time.Duration
	entries      map[string]affinityEntry
	exitIPOwners map[string]map[string]struct{}
}

func newSessionAffinityManager(ttl time.Duration) *sessionAffinityManager {
	if ttl <= 0 {
		ttl = defaultSessionAffinityTTL
	}
	return &sessionAffinityManager{
		ttl:          ttl,
		entries:      make(map[string]affinityEntry),
		exitIPOwners: make(map[string]map[string]struct{}),
	}
}

func (m *sessionAffinityManager) resolve(
	sessionID string,
	excludes []string,
	loader func(string) (*storage.Proxy, error),
	selector func([]string, []string) (*storage.Proxy, error),
) (*storage.Proxy, error) {
	if sessionID == "" {
		return selector(excludes, nil)
	}

	if stickyAddress, ok := m.lookup(sessionID, excludes); ok {
		if proxy, err := loader(stickyAddress); err == nil {
			m.bind(sessionID, stickyAddress, proxy.ExitIP)
			return proxy, nil
		}
		m.delete(sessionID)
	}

	retryExcludes := append([]string(nil), excludes...)
	for {
		blockedExitIPs := m.blockedExitIPs(sessionID)
		proxy, err := selector(retryExcludes, blockedExitIPs)
		if err == nil {
			if m.tryBindUnique(sessionID, proxy.Address, proxy.ExitIP) {
				return proxy, nil
			}
			retryExcludes = append(retryExcludes, proxy.Address)
			continue
		}

		if len(blockedExitIPs) == 0 {
			return nil, err
		}

		// 当池子里没有更多独占出口 IP 时，退化为允许共享，避免直接把新 session 拒掉。
		proxy, err = selector(retryExcludes, nil)
		if err != nil {
			return nil, err
		}
		if !m.tryBindUnique(sessionID, proxy.Address, proxy.ExitIP) {
			m.bind(sessionID, proxy.Address, proxy.ExitIP)
		}
		return proxy, nil
	}
}

func (m *sessionAffinityManager) lookup(sessionID string, excludes []string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cleanupExpiredLocked(time.Now())

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

func (m *sessionAffinityManager) bind(sessionID, address, exitIP string) {
	if sessionID == "" || address == "" {
		return
	}
	m.mu.Lock()
	m.cleanupExpiredLocked(time.Now())
	m.replaceBindingLocked(sessionID, address, exitIP)
	m.mu.Unlock()
}

func (m *sessionAffinityManager) tryBindUnique(sessionID, address, exitIP string) bool {
	if sessionID == "" || address == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.cleanupExpiredLocked(time.Now())
	if exitIP != "" {
		if owners, ok := m.exitIPOwners[exitIP]; ok {
			for owner := range owners {
				if owner != sessionID {
					return false
				}
			}
		}
	}

	m.replaceBindingLocked(sessionID, address, exitIP)
	return true
}

func (m *sessionAffinityManager) blockedExitIPs(sessionID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cleanupExpiredLocked(time.Now())

	blocked := make([]string, 0, len(m.exitIPOwners))
	for exitIP, owners := range m.exitIPOwners {
		if exitIP == "" {
			continue
		}
		for owner := range owners {
			if owner != sessionID {
				blocked = append(blocked, exitIP)
				break
			}
		}
	}
	return blocked
}

func (m *sessionAffinityManager) cleanupExpiredLocked(now time.Time) {
	for sessionID, entry := range m.entries {
		if now.After(entry.expiresAt) {
			m.removeBindingLocked(sessionID, entry)
		}
	}
}

func (m *sessionAffinityManager) replaceBindingLocked(sessionID, address, exitIP string) {
	if existing, ok := m.entries[sessionID]; ok {
		m.removeExitIPOwnerLocked(existing.exitIP, sessionID)
	}

	m.entries[sessionID] = affinityEntry{
		address:   address,
		exitIP:    exitIP,
		expiresAt: time.Now().Add(m.ttl),
	}

	if exitIP != "" {
		owners := m.exitIPOwners[exitIP]
		if owners == nil {
			owners = make(map[string]struct{})
			m.exitIPOwners[exitIP] = owners
		}
		owners[sessionID] = struct{}{}
	}
}

func (m *sessionAffinityManager) removeBindingLocked(sessionID string, entry affinityEntry) {
	delete(m.entries, sessionID)
	m.removeExitIPOwnerLocked(entry.exitIP, sessionID)
}

func (m *sessionAffinityManager) removeExitIPOwnerLocked(exitIP, sessionID string) {
	if exitIP == "" {
		return
	}

	owners, ok := m.exitIPOwners[exitIP]
	if !ok {
		return
	}
	delete(owners, sessionID)
	if len(owners) == 0 {
		delete(m.exitIPOwners, exitIP)
	}
}

func (m *sessionAffinityManager) delete(sessionID string) {
	if sessionID == "" {
		return
	}
	m.mu.Lock()
	if entry, ok := m.entries[sessionID]; ok {
		m.removeBindingLocked(sessionID, entry)
	}
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
