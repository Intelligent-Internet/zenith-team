package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"boardly.dev/boardly/server/internal/contentbundle"
)

// SkillBundleCache is the content-addressed on-disk bundle cache at
// <workspaces-root>/.skill-cache/v1/<company>/<source>/<id>/<hash>/bundle.json.
type SkillBundleCache struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func NewSkillBundleCache(root string) *SkillBundleCache {
	return &SkillBundleCache{root: root, locks: make(map[string]*sync.Mutex)}
}

func (c *SkillBundleCache) Load(companyID string, ref SkillRefData) (SkillData, bool) {
	if c == nil || c.root == "" {
		return SkillData{}, false
	}
	keyPath := c.bundlePath(companyID, ref)
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return SkillData{}, false
	}
	var bundle SkillData
	if err := json.Unmarshal(data, &bundle); err != nil || !validateSkillBundle(ref, bundle) {
		_ = os.Remove(keyPath)
		return SkillData{}, false
	}
	return bundle, true
}

func (c *SkillBundleCache) Store(companyID string, bundle SkillData) error {
	if c == nil || c.root == "" {
		return nil
	}
	ref := SkillRefData{ID: bundle.ID, Source: bundle.Source, Hash: bundle.Hash}
	dir := filepath.Dir(c.bundlePath(companyID, ref))
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".bundle-*")
	if err != nil {
		if mkErr := os.MkdirAll(filepath.Dir(dir), 0o755); mkErr != nil {
			return mkErr
		}
		tmp, err = os.MkdirTemp(filepath.Dir(dir), ".bundle-*")
		if err != nil {
			return err
		}
	}
	defer os.RemoveAll(tmp)

	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "bundle.json"), data, 0o644); err != nil {
		return err
	}
	_ = os.RemoveAll(dir)
	return os.Rename(tmp, dir)
}

func (c *SkillBundleCache) WithRefLock(companyID string, ref SkillRefData, fn func() error) error {
	if c == nil {
		return fn()
	}
	key := companyID + "\x00" + ref.Source + "\x00" + ref.ID + "\x00" + ref.Hash
	lock := c.lockForKey(key)
	lock.Lock()
	defer lock.Unlock()
	return fn()
}

func (c *SkillBundleCache) lockForKey(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if lock := c.locks[key]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	c.locks[key] = lock
	return lock
}

func (c *SkillBundleCache) bundlePath(companyID string, ref SkillRefData) string {
	return filepath.Join(
		c.root,
		safeCacheSegment(companyID),
		safeCacheSegment(ref.Source),
		safeCacheSegment(ref.ID),
		safeCacheSegment(ref.Hash),
		"bundle.json",
	)
}

// validateSkillBundle verifies both frozen identity and the actual cached bytes.
func validateSkillBundle(ref SkillRefData, bundle SkillData) bool {
	return contentbundle.ValidateSkill(ref, bundle) == nil
}

func safeCacheSegment(s string) string {
	var b strings.Builder
	if s == "" {
		return "_"
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return fmt.Sprintf("_%s", out)
	}
	return out
}
