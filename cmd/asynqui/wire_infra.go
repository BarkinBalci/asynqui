package main

import (
	"context"
	"fmt"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web"
)

// The registrySource methods of the Servers, Schedulers and Redis info
// pages (web.ServerSource, web.SchedulerSource, web.InfoSource).

func (s registrySource) Servers(ctx context.Context, name string) ([]web.Server, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return nil, conn.ErrNotFound
	}
	servers, err := c.Servers(ctx)
	if err != nil {
		return nil, webReadErr(err)
	}
	out := make([]web.Server, len(servers))
	for i, sv := range servers {
		ws := web.Server{
			ID:             sv.ID,
			Host:           sv.Host,
			PID:            sv.PID,
			Concurrency:    sv.Concurrency,
			StrictPriority: sv.StrictPriority,
			Started:        sv.Started,
			Status:         sv.Status,
		}
		for _, q := range sv.Queues {
			ws.Queues = append(ws.Queues, web.QueueWeight{Name: q.Name, Weight: q.Weight})
		}
		for _, w := range sv.Workers {
			ws.Workers = append(ws.Workers, web.Worker{
				TaskID: w.TaskID, TaskType: w.TaskType, Queue: w.Queue, Started: w.Started, Deadline: w.Deadline,
			})
		}
		out[i] = ws
	}
	return out, nil
}

func (s registrySource) SchedulerEntries(ctx context.Context, name string) ([]web.SchedulerEntry, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return nil, conn.ErrNotFound
	}
	entries, err := c.SchedulerEntries(ctx)
	if err != nil {
		return nil, webReadErr(err)
	}
	out := make([]web.SchedulerEntry, len(entries))
	for i, e := range entries {
		out[i] = web.SchedulerEntry{
			ID: e.ID, Spec: e.Spec, TaskType: e.TaskType, Payload: e.Payload, Options: webOptions(e.Options),
			Queue: e.Queue, Next: e.Next, Prev: e.Prev,
		}
	}
	return out, nil
}

func (s registrySource) SchedulerEnqueueEvents(ctx context.Context, name, entryID, queue string, page, size int) (web.EnqueuePage, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return web.EnqueuePage{}, conn.ErrNotFound
	}
	p, err := c.SchedulerEnqueueEvents(ctx, entryID, queue, page, size)
	if err != nil {
		return web.EnqueuePage{}, webReadErr(err)
	}
	out := web.EnqueuePage{HasNext: p.HasNext}
	for _, e := range p.Events {
		out.Events = append(out.Events, web.EnqueueEvent{TaskID: e.TaskID, EnqueuedAt: e.EnqueuedAt, TaskExists: e.TaskExists})
	}
	return out, nil
}

func (s registrySource) Info(ctx context.Context, name string) (web.Info, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return web.Info{}, conn.ErrNotFound
	}
	i, err := c.Info(ctx)
	if err != nil {
		return web.Info{}, webReadErr(err)
	}
	tls := c.TLS()
	return web.Info{
		Valkey:           i.Backend == conn.BackendValkey,
		Version:          i.Version,
		RedisVersion:     i.RedisVersion,
		Mode:             i.Mode,
		Uptime:           i.Uptime,
		ConnectedClients: i.ConnectedClients,
		UsedMemory:       i.UsedMemory,
		UsedMemoryPeak:   i.UsedMemoryPeak,
		MaxMemory:        i.MaxMemory,
		MaxMemoryPolicy:  i.MaxMemoryPolicy,
		DB:               i.Keyspace.DB,
		Keys:             i.Keyspace.Keys,
		Expires:          i.Keyspace.Expires,
		TLS:              tls.Enabled,
		TLSVerified:      tls.Verified,
		TLSVersion:       tls.Version,
		Username:         c.Username(),
		Raw:              i.Raw,
	}, nil
}

// webReadErr marks a read that could not reach the server, so the page
// shows the connection down; an error reply stays a plain error.
func webReadErr(err error) error {
	if conn.Unreachable(err) {
		return fmt.Errorf("%w: %w", web.ErrUnreachable, err)
	}
	return err
}

func webOptions(opts []conn.EntryOption) []web.EntryOption {
	out := make([]web.EntryOption, len(opts))
	for i, o := range opts {
		out[i] = web.EntryOption{Name: o.Name, Value: o.Value}
	}
	return out
}

// isValkey reports whether c's backend was detected as Valkey.
func isValkey(c *conn.Conn) bool {
	b, known := c.Backend()
	return known && b == conn.BackendValkey
}
