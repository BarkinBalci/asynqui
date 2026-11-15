package web

import (
	"context"
	"net/http"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
	"github.com/BarkinBalci/asynqui/internal/web/views/pages"
)

// InfoSource is what the Redis info page needs from a connection.
type InfoSource interface {
	// Info reads the instance's INFO. It may be served from a short-lived
	// cache.
	Info(ctx context.Context) (*conn.Info, error)
	// TLS is the connection's TLS setting and last handshake's version.
	TLS() conn.TLSInfo
	// Username is the ACL user; empty is the default user.
	Username() string
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
	name := a.conn.Name()
	ctx, cancel := d.pageContext(r.Context())
	defer cancel()
	info, rd := readLive(ctx, a, a.conn.Info)
	// The label follows the backend detected so far, and this read when it
	// has one.
	valkey := isValkey(a.conn)
	if rd.state == pages.DataReady {
		valkey = info.Backend == conn.BackendValkey
	} else {
		info = &conn.Info{}
	}
	tls := a.conn.TLS()
	crumb := sectionCrumb(d, components.SectionInfo, "c", name, "info")
	crumb.Valkey = valkey
	m := pages.Info{
		Layout: d.liveLayout(r, a, components.SectionInfo, rd, crumb),
		Live:   d.live(ans, pages.InfoPageID, pages.InfoPollID, rd, rd.state == pages.DataReady, nil, "c", name, "info"),
		Addr:   a.conn.Addr(),
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
			DB:               int64(info.Keyspace.DB),
			Keys:             info.Keyspace.Keys,
			Expires:          info.Keyspace.Expires,
			TLS:              tls.Enabled,
			TLSVerified:      tls.Verified,
			TLSVersion:       tls.Version,
			Username:         a.conn.Username(),
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
