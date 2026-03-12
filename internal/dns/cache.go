package localdns

import (
	"net/netip"
	"sync"
	"time"
)

type CacheEntry struct {
	Domain    string
	Addresses []netip.Addr
	ExpiresAt time.Time
}

type Cache struct {
	mu       sync.RWMutex
	byDomain map[string]CacheEntry
	byAddr   map[string]string
}

func NewCache() *Cache {
	return &Cache{
		byDomain: map[string]CacheEntry{},
		byAddr:   map[string]string{},
	}
}

func (c *Cache) Set(domain string, addrs []netip.Addr, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.byDomain[domain]; ok {
		for _, addr := range existing.Addresses {
			delete(c.byAddr, addr.String())
		}
	}
	entry := CacheEntry{
		Domain:    domain,
		Addresses: append([]netip.Addr(nil), addrs...),
		ExpiresAt: time.Now().Add(ttl),
	}
	c.byDomain[domain] = entry
	for _, addr := range addrs {
		c.byAddr[addr.String()] = domain
	}
}

func (c *Cache) LookupDomain(domain string) ([]netip.Addr, bool) {
	c.mu.RLock()
	entry, ok := c.byDomain[domain]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		return nil, false
	}
	return append([]netip.Addr(nil), entry.Addresses...), true
}

func (c *Cache) LookupAddr(addr netip.Addr) (string, bool) {
	c.mu.RLock()
	domain, ok := c.byAddr[addr.String()]
	if !ok {
		c.mu.RUnlock()
		return "", false
	}
	entry, ok := c.byDomain[domain]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		return "", false
	}
	for _, candidate := range entry.Addresses {
		if candidate == addr {
			return domain, true
		}
	}
	return "", false
}

func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byDomain = map[string]CacheEntry{}
	c.byAddr = map[string]string{}
}
