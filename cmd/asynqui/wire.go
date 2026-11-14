package main

import (
	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web"
)

// registrySource lists the registry's connections for web, which uses
// each *conn.Conn through its own interfaces.
type registrySource struct {
	reg *conn.Registry
}

func (s registrySource) Connections() []web.Conn {
	all := s.reg.All()
	out := make([]web.Conn, len(all))
	for i, c := range all {
		out[i] = c
	}
	return out
}
