package forge

import (
	"encoding/json"
	"os"
	"time"
)

// cacheTTL is how long a detection result is trusted: logging in to a host
// or installing a plugin changes the answer.
const cacheTTL = 24 * time.Hour

// FileCache is a DetectCache in a JSON file, e.g. in the git common dir.
type FileCache struct {
	Path string
	Now  func() time.Time
}

type cacheEntry struct {
	Provider string    `json:"provider"`
	At       time.Time `json:"at"`
}

func (c FileCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c FileCache) load() map[string]cacheEntry {
	m := map[string]cacheEntry{}
	if data, err := os.ReadFile(c.Path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// Get returns the provider recorded for host, if it is recent.
func (c FileCache) Get(host string) (string, bool) {
	e, ok := c.load()[host]
	if !ok || c.now().Sub(e.At) > cacheTTL {
		return "", false
	}
	return e.Provider, true
}

// Set records provider for host. Failures are ignored: the cache only saves time.
func (c FileCache) Set(host, provider string) {
	m := c.load()
	m[host] = cacheEntry{Provider: provider, At: c.now()}
	if data, err := json.MarshalIndent(m, "", "  "); err == nil {
		_ = os.WriteFile(c.Path, append(data, '\n'), 0o644)
	}
}
