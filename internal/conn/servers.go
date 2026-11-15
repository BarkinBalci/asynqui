package conn

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hibiken/asynq"
)

// serverInspector is the part of *asynq.Inspector that the Servers pages
// use.
type serverInspector interface {
	Servers() ([]*asynq.ServerInfo, error)
}

// Server is one asynq server (a worker process) of a connection, from
// asynq's ServerInfo.
type Server struct {
	ID   string
	Host string
	PID  int
	// Concurrency is how many tasks the server processes at a time.
	Concurrency int
	// Queues are the queues the server takes tasks from, by weight
	// (highest first), then name.
	Queues         []QueueWeight
	StrictPriority bool
	Started        time.Time
	// Status is asynq's server status: "active", or "stopped" once the
	// server stopped taking new tasks.
	Status string
	// Workers are the tasks in progress, oldest first.
	Workers []Worker
}

// QueueWeight is a queue a server takes tasks from, with its priority.
type QueueWeight struct {
	Name   string
	Weight int
}

// Worker is a task a server is processing.
type Worker struct {
	TaskID   string
	TaskType string
	Queue    string
	Started  time.Time
	// Deadline is when the task must be done by.
	Deadline time.Time
}

// Servers returns the connection's asynq servers, sorted by host, PID and ID,
// from the cache. The slice is shared between callers, who must not modify it.
func (c *Conn) Servers(ctx context.Context) ([]Server, error) {
	return cached(ctx, c.cache, serversKey, cacheTTL, c.fetchServers)
}

func (c *Conn) fetchServers(context.Context) ([]Server, error) {
	infos, err := c.servers.Servers()
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	out := make([]Server, 0, len(infos))
	for _, si := range infos {
		s := Server{
			ID:             si.ID,
			Host:           si.Host,
			PID:            si.PID,
			Concurrency:    si.Concurrency,
			StrictPriority: si.StrictPriority,
			Started:        si.Started,
			Status:         si.Status,
		}
		for _, q := range slices.Sorted(maps.Keys(si.Queues)) {
			s.Queues = append(s.Queues, QueueWeight{Name: q, Weight: si.Queues[q]})
		}
		slices.SortStableFunc(s.Queues, func(a, b QueueWeight) int { return cmp.Compare(b.Weight, a.Weight) })
		for _, w := range si.ActiveWorkers {
			s.Workers = append(s.Workers, Worker{
				TaskID:   w.TaskID,
				TaskType: w.TaskType,
				Queue:    w.Queue,
				Started:  w.Started,
				Deadline: w.Deadline,
			})
		}
		slices.SortStableFunc(s.Workers, func(a, b Worker) int {
			return cmp.Or(a.Started.Compare(b.Started), strings.Compare(a.TaskID, b.TaskID))
		})
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Server) int {
		return cmp.Or(strings.Compare(a.Host, b.Host), cmp.Compare(a.PID, b.PID), strings.Compare(a.ID, b.ID))
	})
	return out, nil
}
