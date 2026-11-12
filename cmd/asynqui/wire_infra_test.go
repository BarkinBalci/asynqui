package main

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web"
)

// Only a read that could not reach Redis shows the connection down.
func TestWebReadErr(t *testing.T) {
	refused := fmt.Errorf("list servers: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	if err := webReadErr(refused); !errors.Is(err, web.ErrUnreachable) || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("network error = %v", err)
	}
	reply := errors.New("NOPERM no permissions")
	if err := webReadErr(reply); errors.Is(err, web.ErrUnreachable) || !errors.Is(err, reply) {
		t.Errorf("error reply = %v", err)
	}
	if got := webOptions([]conn.EntryOption{{Name: "queue", Value: "critical"}}); len(got) != 1 || got[0] != (web.EntryOption{Name: "queue", Value: "critical"}) {
		t.Errorf("options = %+v", got)
	}
}
