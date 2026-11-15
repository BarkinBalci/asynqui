package web

import (
	"github.com/BarkinBalci/asynqui/internal/conn"
)

// ConnectionSource lists the configured connections in config order.
type ConnectionSource interface {
	Connections() []Conn
}

// Conn is one connection as the web layer uses it. Each area declares its
// reads and actions next to its handlers.
type Conn interface {
	Name() string
	// Addr is the display address, "host:port/db".
	Addr() string
	// Health is the cached result of the last health check; no I/O.
	Health() conn.Health
	// Backend is the server software detected so far.
	Backend() (conn.Backend, bool)
	OverviewSource
	QueueSource
	TaskSource
	ServerSource
	SchedulerSource
	InfoSource
}

// isValkey reports whether c's backend was detected as Valkey.
func isValkey(c Conn) bool {
	b, _ := c.Backend()
	return b == conn.BackendValkey
}
