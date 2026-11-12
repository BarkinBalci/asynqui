package conn

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
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
// connections (other DBs) may share, except Keyspace, which is the
// connection's own DB. It is shared between callers, who must not modify
// it.
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
	// Mode is redis_mode, or server_mode on a Valkey server that writes
	// no redis_mode ("standalone").
	Mode string
	// Uptime is uptime_in_seconds.
	Uptime time.Duration
	// ConnectedClients is connected_clients.
	ConnectedClients int64
	// UsedMemory, UsedMemoryPeak and MaxMemory are used_memory,
	// used_memory_peak and maxmemory, in bytes. MaxMemory 0 means no
	// limit.
	UsedMemory     int64
	UsedMemoryPeak int64
	MaxMemory      int64
	// MaxMemoryPolicy is maxmemory_policy.
	MaxMemoryPolicy string
	// Keyspace is the Keyspace line of the connection's own DB.
	Keyspace Keyspace
	// Sections are in reply order, each with its fields in reply order.
	Sections []InfoSection
	// Raw is the reply as received, without the Keyspace and Keysizes
	// lines of other DBs ("db3:…", "db3_distrib_…"): another connection
	// may own them. Sections keeps every line.
	Raw string
}

// Keyspace is one DB's line of the Keyspace section
// ("db3:keys=7,expires=1,avg_ttl=0"). An empty DB has no line: zero keys.
type Keyspace struct {
	DB      int
	Keys    int64
	Expires int64
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

// infoReader sends INFO.
type infoReader interface {
	Info(ctx context.Context, sections ...string) (string, error)
}

type redisInfo struct{ c redis.UniversalClient }

func (r redisInfo) Info(ctx context.Context, sections ...string) (string, error) {
	return r.c.Info(ctx, sections...).Result()
}

// Info returns the server's INFO (default sections), from the 1s cache
// when fresh. Each fetch also updates Backend.
func (c *Conn) Info(ctx context.Context) (*Info, error) {
	return cached(ctx, c.cache, infoKey, cacheTTL, c.fetchInfo)
}

func (c *Conn) fetchInfo(ctx context.Context) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	raw, err := c.info.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("info: %w", err)
	}
	info := parseInfo(raw, c.db)
	c.setBackend(info.Backend)
	return info, nil
}

// Backend returns the server software found by the last INFO, and false
// before any. It does no I/O: the health loop detects the backend each
// time the connection becomes reachable, and every Info fetch refreshes
// it.
func (c *Conn) Backend() (Backend, bool) {
	switch c.backend.Load() {
	case backendRedis:
		return BackendRedis, true
	case backendValkey:
		return BackendValkey, true
	}
	return BackendRedis, false
}

// Stored values of Conn.backend; 0 is not detected yet.
const (
	backendRedis int32 = 1 + iota
	backendValkey
)

func (c *Conn) setBackend(b Backend) {
	v := backendRedis
	if b == BackendValkey {
		v = backendValkey
	}
	c.backend.Store(v)
}

// detectBackend reads INFO server and stores the backend. A failure keeps
// the last value; the next detection tries again.
func (c *Conn) detectBackend(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	raw, err := c.info.Info(ctx, "server")
	if err != nil {
		c.logger.Debug("backend detection failed", slog.Any("error", err))
		return
	}
	c.setBackend(parseInfo(raw, c.db).Backend)
}

// parseInfo parses an INFO reply: "# Section" headers followed by key:value
// lines, split on the first colon. Lines before the first header go in a
// section named "". Blank and malformed lines are skipped. db is the
// connection's DB, whose Keyspace line fills Keyspace. Numbers that do not
// parse are left zero.
func parseInfo(raw string, db int) *Info {
	info := &Info{Raw: rawForDB(raw, db), Keyspace: Keyspace{DB: db}}
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
	info.Mode, _ = server.Get("redis_mode")
	if info.Mode == "" {
		info.Mode, _ = server.Get("server_mode")
	}
	info.Uptime = time.Duration(fieldInt(server, "uptime_in_seconds")) * time.Second
	clients, _ := info.Section("Clients")
	info.ConnectedClients = fieldInt(clients, "connected_clients")
	mem, _ := info.Section("Memory")
	info.UsedMemory = fieldInt(mem, "used_memory")
	info.UsedMemoryPeak = fieldInt(mem, "used_memory_peak")
	info.MaxMemory = fieldInt(mem, "maxmemory")
	info.MaxMemoryPolicy, _ = mem.Get("maxmemory_policy")
	ks, _ := info.Section("Keyspace")
	if line, ok := ks.Get("db" + strconv.Itoa(db)); ok {
		for kv := range strings.SplitSeq(line, ",") {
			k, v, _ := strings.Cut(kv, "=")
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				continue
			}
			switch k {
			case "keys":
				info.Keyspace.Keys = n
			case "expires":
				info.Keyspace.Expires = n
			}
		}
	}
	return info
}

// rawForDB drops, from the Keyspace and Keysizes sections, the lines of
// DBs other than db, keeping everything else byte for byte.
func rawForDB(raw string, db int) string {
	own := "db" + strconv.Itoa(db)
	var b strings.Builder
	b.Grow(len(raw))
	perDB := false
	for line := range strings.Lines(raw) {
		key, _, _ := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
		if name, ok := strings.CutPrefix(key, "#"); ok {
			name = strings.TrimSpace(name)
			perDB = strings.EqualFold(name, "Keyspace") || strings.EqualFold(name, "Keysizes")
		} else if perDB && otherDB(key, own) {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// otherDB reports whether key is "dbN" or "dbN_…" for a DB other than own.
func otherDB(key, own string) bool {
	rest, ok := strings.CutPrefix(key, "db")
	if !ok {
		return false
	}
	n := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if n < 0 {
		n = len(rest)
	}
	if n == 0 || (n < len(rest) && rest[n] != '_') {
		return false
	}
	return "db"+rest[:n] != own
}

// fieldInt is the integer value of key in s, or 0.
func fieldInt(s InfoSection, key string) int64 {
	v, _ := s.Get(key)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
