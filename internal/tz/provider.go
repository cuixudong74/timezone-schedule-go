package tz

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	// Embed the IANA tz database so the binary works without host zoneinfo.
	_ "time/tzdata"
)

// EmbeddedProvider uses the IANA tz database embedded in the binary.
// The rules version is fixed at build time; operators may override the
// reported label via configuration.
type EmbeddedProvider struct {
	version string
}

// NewEmbeddedProvider returns an EmbeddedProvider reporting version.
// If version is empty, a default label is used.
func NewEmbeddedProvider(version string) *EmbeddedProvider {
	if version == "" {
		version = "embedded"
	}
	return &EmbeddedProvider{version: version}
}

// Resolve loads the named location from the embedded database.
func (p *EmbeddedProvider) Resolve(name string) (*time.Location, error) {
	if name == "" {
		return nil, ErrInvalidTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTimezone, name)
	}
	return loc, nil
}

// Version reports the embedded tzdata version label.
func (p *EmbeddedProvider) Version() string { return p.version }

// ReloadableProvider loads zoneinfo files from a directory and can be
// reloaded at runtime, modelling timezone rule updates. Previously
// returned *time.Location values remain valid (they are immutable
// snapshots); subsequent resolves use the new rules.
type ReloadableProvider struct {
	dir     string
	mu      sync.RWMutex
	version string
	cache   map[string]*time.Location
}

// NewReloadableProvider loads zoneinfo from dir.
func NewReloadableProvider(dir string) (*ReloadableProvider, error) {
	if dir == "" {
		return nil, ErrInvalidTimezone
	}
	p := &ReloadableProvider{dir: dir, cache: map[string]*time.Location{}}
	if err := p.reloadLocked(); err != nil {
		return nil, err
	}
	return p, nil
}

// Resolve loads the named location from the current rules.
func (p *ReloadableProvider) Resolve(name string) (*time.Location, error) {
	if name == "" {
		return nil, ErrInvalidTimezone
	}
	p.mu.RLock()
	loc, ok := p.cache[name]
	p.mu.RUnlock()
	if ok {
		return loc, nil
	}
	data, err := os.ReadFile(filepath.Join(p.dir, filepath.FromSlash(name)))
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTimezone, name)
	}
	loc, err = time.LoadLocationFromTZData(name, data)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidTimezone, name)
	}
	p.mu.Lock()
	p.cache[name] = loc
	p.mu.Unlock()
	return loc, nil
}

// Reload re-reads the zoneinfo directory. It returns true if the
// rules version changed.
func (p *ReloadableProvider) Reload() (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.version
	if err := p.reloadLocked(); err != nil {
		return false, err
	}
	return p.version != old, nil
}

// Version reports the current rules version label.
func (p *ReloadableProvider) Version() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.version
}

func (p *ReloadableProvider) reloadLocked() error {
	version := p.readVersion()
	p.version = version
	p.cache = map[string]*time.Location{}
	return nil
}

// readVersion reads a VERSION file if present, otherwise hashes the
// directory entries so rule changes are observable.
func (p *ReloadableProvider) readVersion() string {
	if data, err := os.ReadFile(filepath.Join(p.dir, "VERSION")); err == nil {
		v := strings.TrimSpace(string(data))
		if v != "" {
			return v
		}
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return "unknown"
	}
	h := sha256.New()
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "%s:%d:%d\n", e.Name(), fi.ModTime().UnixNano(), fi.Size())
	}
	return "dir:" + hex.EncodeToString(h.Sum(nil))[:12]
}
