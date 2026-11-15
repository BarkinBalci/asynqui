package conn

import (
	"cmp"
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
	// Raw is the reply as received, without the Keyspace and Keysizes
	// lines of other DBs ("db3:…", "db3_distrib_…"): another connection
	// may own them.
	Raw string
}

// Keyspace is one DB's line of the Keyspace section
// ("db3:keys=7,expires=1,avg_ttl=0"). An empty DB has no line: zero keys.
type Keyspace struct {
	DB      int
	Keys    int64
	Expires int64
}

// infoReader sends INFO.
type infoReader interface {
	Info(ctx context.Context, sections ...string) (string, error)
}

type redisInfo struct{ c redis.UniversalClient }

func (r redisInfo) Info(ctx context.Context, sections ...string) (string, error) {
	return r.c.Info(ctx, sections...).Result()
}

// Info returns the server's INFO (default sections), from the cache. Each
// fetch also updates Backend.
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

// Backend returns the server software found by the last INFO, and false before
// any. It does no I/O.
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

// parseInfo parses an INFO reply. db is the connection's DB, whose Keyspace
// line fills Keyspace. Malformed lines are skipped and numbers that do not
// parse are left zero.
func parseInfo(raw string, db int) *Info {
	info := &Info{Raw: rawForDB(raw, db), Keyspace: Keyspace{DB: db}}
	fields := map[string]string{} // by "section/key", the section lowercased
	section := ""
	for line := range strings.Lines(raw) {
		line = strings.TrimRight(line, "\r\n")
		if name, ok := strings.CutPrefix(line, "#"); ok {
			section = strings.ToLower(strings.TrimSpace(name))
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if _, dup := fields[section+"/"+k]; ok && k != "" && !dup {
			fields[section+"/"+k] = v
		}
	}
	num := func(key string) int64 {
		n, err := strconv.ParseInt(fields[key], 10, 64)
		if err != nil {
			return 0
		}
		return n
	}

	info.RedisVersion = fields["server/redis_version"]
	info.Version = info.RedisVersion
	if fields["server/server_name"] == "valkey" {
		info.Backend = BackendValkey
		if v, ok := fields["server/valkey_version"]; ok {
			info.Version = v
		}
	}
	info.Mode = cmp.Or(fields["server/redis_mode"], fields["server/server_mode"])
	info.Uptime = time.Duration(num("server/uptime_in_seconds")) * time.Second
	info.ConnectedClients = num("clients/connected_clients")
	info.UsedMemory = num("memory/used_memory")
	info.UsedMemoryPeak = num("memory/used_memory_peak")
	info.MaxMemory = num("memory/maxmemory")
	info.MaxMemoryPolicy = fields["memory/maxmemory_policy"]
	for kv := range strings.SplitSeq(fields["keyspace/db"+strconv.Itoa(db)], ",") {
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
