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
//
// The fixtures were built from the INFO code of Redis 7.4.1 and Valkey 8.0
// (src/server.c): every field a fresh standalone master writes in the
// default sections, in order, with made-up values. Regenerate them from
// real containers (redis:7.4-alpine, valkey/valkey:8.0-alpine; INFO with no
// argument) once Docker is available.
func readInfoFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\n", "\r\n")
}

func TestParseInfoFixtures(t *testing.T) {
	tests := []struct {
		fixture string
		want    Info
	}{
		{"info_redis.txt", Info{Backend: BackendRedis, Version: "7.4.1", RedisVersion: "7.4.1", Mode: "standalone",
			MaxMemoryPolicy: "noeviction", Keyspace: Keyspace{Keys: 128, Expires: 4}}},
		// Valkey writes server_mode, not redis_mode, unless
		// extended-redis-compatibility is on (it is off by default).
		{"info_valkey.txt", Info{Backend: BackendValkey, Version: "8.0.2", RedisVersion: "7.2.4", Mode: "standalone",
			MaxMemoryPolicy: "noeviction", Keyspace: Keyspace{Keys: 3, Expires: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			raw := readInfoFixture(t, tt.fixture)
			info := parseInfo(raw, 0)
			// Raw is the reply without other DBs' keyspace lines.
			if want := strings.Replace(raw, "db2:keys=7,expires=0,avg_ttl=0,subexpiry=0\r\n", "", 1); info.Raw != want {
				t.Error("Raw is not the reply as received, less db2")
			}
			got := Info{Backend: info.Backend, Version: info.Version, RedisVersion: info.RedisVersion, Mode: info.Mode,
				MaxMemoryPolicy: info.MaxMemoryPolicy, Keyspace: info.Keyspace}
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
			name: "first colon splits, first key wins, junk skipped, section names case-insensitive",
			raw:  "# server\r\nredis_version:7.0.0\r\nredis_mode:a:b\r\nredis_version:9\r\nnot a field\r\n:novalue\r\n\r\n#Keyspace\r\ndb1:keys=1,expires=0\r\ndb0:keys=2,expires=0\r\n",
			want: &Info{
				Version:      "7.0.0",
				RedisVersion: "7.0.0",
				Mode:         "a:b",
				Keyspace:     Keyspace{Keys: 2},
			},
		},
		{
			name: "fields before any header, LF endings",
			raw:  "redis_version:6.2.0\nrole:master\n",
			want: &Info{},
		},
		{
			name: "valkey without valkey_version",
			raw:  "# Server\r\nredis_version:7.2.4\r\nserver_name:valkey\r\n",
			want: &Info{Backend: BackendValkey, Version: "7.2.4", RedisVersion: "7.2.4"},
		},
		{
			name: "server_name other than valkey",
			raw:  "# Server\r\nredis_version:7.2.4\r\nserver_name:keydb\r\n",
			want: &Info{Version: "7.2.4", RedisVersion: "7.2.4"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.want.Raw = strings.Replace(tt.raw, "db1:keys=1,expires=0\r\n", "", 1) // db 0's view
			if diff := cmp.Diff(tt.want, parseInfo(tt.raw, 0)); diff != "" {
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
