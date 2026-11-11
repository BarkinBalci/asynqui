package web

import (
	"context"
	"net/http"
	"time"

	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// InfoSource is what the Redis info page needs from the connection
// registry.
type InfoSource interface {
	// Info reads the instance's INFO. It may be served from a
	// short-lived cache.
	Info(ctx context.Context, name string) (Info, error)
}

// Info is a connection's instance information. Everything but the
// keyspace numbers, TLS and Username describes the whole instance, which
// other connections (other DBs) may share; the page is about the instance,
// so it shows them.
type Info struct {
	Valkey bool
	// Version is the Valkey version for Valkey, else the Redis version;
	// RedisVersion is redis_version.
	Version      string
	RedisVersion string
	Mode         string
	Uptime       time.Duration
	// ConnectedClients and the memory figures (bytes) are instance-wide.
	// MaxMemory 0 means no limit.
	ConnectedClients int64
	UsedMemory       int64
	UsedMemoryPeak   int64
	MaxMemory        int64
	MaxMemoryPolicy  string
	// DB, Keys and Expires are the connection's own DB.
	DB      int
	Keys    int64
	Expires int64
	// TLS and TLSVerified are the connection's TLS settings; Username its
	// ACL user (empty for the default user).
	TLS         bool
	TLSVerified bool
	Username    string
	// Raw is the full INFO reply.
	Raw string
}

// handleInfo serves GET /c/{conn}/info, the Redis / Valkey info page.
// With HX-Request (a poll) it answers with out-of-band swaps.
func handleInfo(d *deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := accessFrom(r.Context())
		m := d.infoModel(r, a, liveAnswer(r, pages.InfoPollID))
		if isFragment(r) {
			d.render(w, r, http.StatusOK, pages.InfoPoll(m))
			return
		}
		d.render(w, r, http.StatusOK, pages.InfoPage(m))
	})
}

func (d *deps) infoModel(r *http.Request, a access, ans answer) pages.Info {
	name := a.conn.Name
	info, rd := readLive(d, r, a, func(ctx context.Context) (Info, error) { return d.conns.Info(ctx, name) })
	// The label follows the backend detected so far, and this read when it
	// has one.
	valkey := a.conn.Valkey
	if rd.state == pages.DataReady {
		valkey = info.Valkey
	}
	crumb := sectionCrumb(d, components.SectionInfo, "c", name, "info")
	crumb.Valkey = valkey
	m := pages.Info{
		Layout: d.liveLayout(r, a, components.SectionInfo, rd, crumb),
		Live:   d.live(ans, pages.InfoPageID, pages.InfoPollID, rd, nil, "c", name, "info"),
		Addr:   a.conn.Addr,
		Valkey: valkey,
		Data: pages.InfoData{
			Version:          info.Version,
			RedisVersion:     info.RedisVersion,
			Mode:             info.Mode,
			Uptime:           info.Uptime,
			ConnectedClients: info.ConnectedClients,
			UsedMemory:       info.UsedMemory,
			UsedMemoryPeak:   info.UsedMemoryPeak,
			MaxMemory:        info.MaxMemory,
			MaxMemoryPolicy:  info.MaxMemoryPolicy,
			DB:               int64(info.DB),
			Keys:             info.Keys,
			Expires:          info.Expires,
			TLS:              info.TLS,
			TLSVerified:      info.TLSVerified,
			Username:         info.Username,
			Raw:              info.Raw,
		},
	}
	for i := range m.Layout.Sidebar.Conns {
		if c := &m.Layout.Sidebar.Conns[i]; c.Current {
			c.Sub.Valkey = valkey
		}
	}
	return m
}
