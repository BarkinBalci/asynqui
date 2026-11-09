package conn

import (
	"context"
	"fmt"
	"strings"
)

// Backend is the server software behind a connection.
type Backend int

// Backends.
const (
	BackendRedis Backend = iota
	BackendValkey
)

// String returns "redis" or "valkey".
func (b Backend) String() string {
	if b == BackendValkey {
		return "valkey"
	}
	return "redis"
}

// Info is a parsed INFO reply. It describes the whole instance, which other
// connections (other DBs) may share. It is shared between callers, who must
// not modify it.
type Info struct {
	// Backend is BackendValkey when the server section has
	// server_name:valkey, otherwise BackendRedis.
	Backend Backend
	// Version is valkey_version for Valkey (redis_version if a Valkey
	// server has none), otherwise redis_version.
	Version string
	// RedisVersion is redis_version: the Redis version a Valkey server is
	// compatible with, or the version of Redis itself.
	RedisVersion string
	// Sections are in reply order, each with its fields in reply order.
	Sections []InfoSection
	// Raw is the reply as received.
	Raw string
}

// InfoSection is one "# Name" section of an INFO reply.
type InfoSection struct {
	Name   string
	Fields []InfoField
}

// InfoField is one key:value line of an INFO reply.
type InfoField struct {
	Key   string
	Value string
}

// Section returns the section with the given name, compared case-
// insensitively.
func (i *Info) Section(name string) (InfoSection, bool) {
	for _, s := range i.Sections {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	return InfoSection{}, false
}

// Get returns the value of key in the section.
func (s InfoSection) Get(key string) (string, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return "", false
}

// Info returns the server's INFO (default sections), from the 1s cache
// when fresh.
func (c *Conn) Info(ctx context.Context) (*Info, error) {
	return cached(ctx, c.cache, infoKey, cacheTTL, c.fetchInfo)
}

func (c *Conn) fetchInfo(ctx context.Context) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	raw, err := c.redis.Info(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("info: %w", err)
	}
	return parseInfo(raw), nil
}

// parseInfo parses an INFO reply: "# Section" headers followed by key:value
// lines, split on the first colon. Lines before the first header go in a
// section named "". Blank and malformed lines are skipped.
func parseInfo(raw string) *Info {
	info := &Info{Raw: raw}
	var cur *InfoSection
	for line := range strings.Lines(raw) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if name, ok := strings.CutPrefix(line, "#"); ok {
			info.Sections = append(info.Sections, InfoSection{Name: strings.TrimSpace(name)})
			cur = &info.Sections[len(info.Sections)-1]
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || k == "" {
			continue
		}
		if cur == nil {
			info.Sections = append(info.Sections, InfoSection{})
			cur = &info.Sections[len(info.Sections)-1]
		}
		cur.Fields = append(cur.Fields, InfoField{Key: k, Value: v})
	}

	server, _ := info.Section("Server")
	info.RedisVersion, _ = server.Get("redis_version")
	info.Version = info.RedisVersion
	if name, _ := server.Get("server_name"); name == "valkey" {
		info.Backend = BackendValkey
		if v, ok := server.Get("valkey_version"); ok {
			info.Version = v
		}
	}
	return info
}
