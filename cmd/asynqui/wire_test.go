package main

import (
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/BarkinBalci/asynqui/internal/config"
	"github.com/BarkinBalci/asynqui/internal/conn"
)

// closedAddr returns a local address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestRegistrySource(t *testing.T) {
	addr := closedAddr(t)
	reg, err := conn.NewRegistry([]config.Connection{
		{Name: "a", Addr: addr, DB: 2, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
		{Name: "b", Addr: addr, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	got := registrySource{reg: reg}.Connections()
	if len(got) != 2 || got[0].Name() != "a" || got[0].Addr() != addr+"/2" || got[1].Name() != "b" {
		t.Fatalf("Connections = %+v", got)
	}
}
