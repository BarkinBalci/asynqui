package conn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// readInfoFixture returns a testdata INFO reply with Redis's CRLF line
// endings.
func readInfoFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\n", "\r\n")
}

func TestParseInfoFixtures(t *testing.T) {
	type summary struct {
		Backend      Backend
		Version      string
		RedisVersion string
		Sections     []string
		Picks        map[string]string // "section/key" → value
	}
	tests := []struct {
		fixture string
		picks   []string
		want    summary
	}{
		{
			fixture: "info_redis.txt",
			picks:   []string{"server/redis_mode", "server/os", "server/config_file", "server/listener0", "memory/maxmemory_policy", "keyspace/db0", "keyspace/db2", "errorstats/errorstat_ERR"},
			want: summary{
				Backend:      BackendRedis,
				Version:      "7.4.1",
				RedisVersion: "7.4.1",
				Sections:     []string{"Server", "Clients", "Memory", "Persistence", "Stats", "Replication", "CPU", "Modules", "Errorstats", "Cluster", "Keyspace"},
				Picks: map[string]string{
					"server/redis_mode":        "standalone",
					"server/os":                "Linux 6.6.87.2-microsoft-standard-WSL2 x86_64",
					"server/config_file":       "",
					"server/listener0":         "name=tcp,bind=*,bind=-::*,port=6379",
					"memory/maxmemory_policy":  "noeviction",
					"keyspace/db0":             "keys=128,expires=4,avg_ttl=3600000,subexpiry=0",
					"keyspace/db2":             "keys=7,expires=0,avg_ttl=0,subexpiry=0",
					"errorstats/errorstat_ERR": "count=2",
				},
			},
		},
		{
			fixture: "info_valkey.txt",
			picks:   []string{"server/server_name", "server/redis_mode", "memory/maxmemory_policy", "keyspace/db0"},
			want: summary{
				Backend:      BackendValkey,
				Version:      "8.0.2",
				RedisVersion: "7.2.4",
				Sections:     []string{"Server", "Clients", "Memory", "Keyspace"},
				Picks: map[string]string{
					"server/server_name":      "valkey",
					"server/redis_mode":       "standalone",
					"memory/maxmemory_policy": "allkeys-lru",
					"keyspace/db0":            "keys=3,expires=1,avg_ttl=59000",
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			raw := readInfoFixture(t, tt.fixture)
			info := parseInfo(raw)
			if info.Raw != raw {
				t.Error("Raw is not the reply as received")
			}
			got := summary{Backend: info.Backend, Version: info.Version, RedisVersion: info.RedisVersion, Picks: map[string]string{}}
			for _, s := range info.Sections {
				got.Sections = append(got.Sections, s.Name)
			}
			for _, p := range tt.picks {
				name, key, _ := strings.Cut(p, "/")
				s, ok := info.Section(name)
				if !ok {
					t.Errorf("no section %q", name)
					continue
				}
				if v, ok := s.Get(key); ok {
					got.Picks[p] = v
				}
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("parseInfo (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseInfoOrderAndEdges(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want *Info
	}{
		{
			name: "empty",
			raw:  "",
			want: &Info{},
		},
		{
			name: "order kept, first colon splits, junk skipped",
			raw:  "# Server\r\nredis_version:7.0.0\r\nexecutable:/opt/a:b\r\nnot a field\r\n:novalue\r\nempty:\r\n\r\n#Keyspace\r\ndb1:keys=1,expires=0\r\ndb0:keys=2,expires=0\r\n",
			want: &Info{
				Version:      "7.0.0",
				RedisVersion: "7.0.0",
				Sections: []InfoSection{
					{Name: "Server", Fields: []InfoField{{"redis_version", "7.0.0"}, {"executable", "/opt/a:b"}, {"empty", ""}}},
					{Name: "Keyspace", Fields: []InfoField{{"db1", "keys=1,expires=0"}, {"db0", "keys=2,expires=0"}}},
				},
			},
		},
		{
			name: "fields before any header, LF endings",
			raw:  "redis_version:6.2.0\nrole:master\n",
			want: &Info{
				Sections: []InfoSection{{Fields: []InfoField{{"redis_version", "6.2.0"}, {"role", "master"}}}},
			},
		},
		{
			name: "valkey without valkey_version",
			raw:  "# Server\r\nredis_version:7.2.4\r\nserver_name:valkey\r\n",
			want: &Info{
				Backend:      BackendValkey,
				Version:      "7.2.4",
				RedisVersion: "7.2.4",
				Sections:     []InfoSection{{Name: "Server", Fields: []InfoField{{"redis_version", "7.2.4"}, {"server_name", "valkey"}}}},
			},
		},
		{
			name: "server_name other than valkey",
			raw:  "# Server\r\nredis_version:7.2.4\r\nserver_name:keydb\r\n",
			want: &Info{
				Version:      "7.2.4",
				RedisVersion: "7.2.4",
				Sections:     []InfoSection{{Name: "Server", Fields: []InfoField{{"redis_version", "7.2.4"}, {"server_name", "keydb"}}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want.Raw = tt.raw
			if diff := cmp.Diff(tt.want, parseInfo(tt.raw)); diff != "" {
				t.Errorf("parseInfo (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBackendString(t *testing.T) {
	if BackendRedis.String() != "redis" || BackendValkey.String() != "valkey" {
		t.Errorf("Backend strings = %q, %q", BackendRedis, BackendValkey)
	}
}
