package sandbox

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
)

// Cache maps an environment hash to the image or volume snapshot that holds its setup result.
// With a path it is kept in a JSON file, so a restart reuses it; with "" it lives in memory.
type Cache struct {
	mu   sync.Mutex
	path string
	m    map[string]string
}

// OpenCache opens the cache at path. A missing file is an empty cache.
func OpenCache(path string) (*Cache, error) {
	c := &Cache{path: path, m: map[string]string{}}
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	return c, json.Unmarshal(b, &c.m)
}

// Get returns the snapshot for hash.
func (c *Cache) Get(hash string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[hash]
	return v, ok
}

// Put records the snapshot for hash.
func (c *Cache) Put(hash, snapshot string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[hash] = snapshot
	if c.path == "" {
		return nil
	}
	b, err := json.Marshal(c.m)
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
