package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/BarkinBalci/asynqui/internal/conn"
	"github.com/BarkinBalci/asynqui/internal/web"
)

// The Queue and Task pages' part of registrySource (web.TaskSource).

func (s registrySource) QueueInfo(ctx context.Context, name, queue string) (web.QueueInfo, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return web.QueueInfo{}, conn.ErrNotFound
	}
	q, err := c.QueueInfo(ctx, queue)
	if err != nil {
		return web.QueueInfo{}, webTaskErr(err)
	}
	return web.QueueInfo{
		Queue: web.Queue{
			Name:           q.Name,
			Paused:         q.Paused,
			Size:           q.Size,
			Pending:        q.Pending,
			Active:         q.Active,
			Scheduled:      q.Scheduled,
			Retry:          q.Retry,
			Archived:       q.Archived,
			ProcessedToday: q.ProcessedToday,
			FailedToday:    q.FailedToday,
			Latency:        q.Latency,
			MemoryUsage:    q.MemoryUsage,
		},
		Completed:   q.Completed,
		Aggregating: q.Aggregating,
	}, nil
}

func (s registrySource) Tasks(ctx context.Context, name, queue string, state web.TaskState, page, size int, desc bool) ([]web.Task, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return nil, conn.ErrNotFound
	}
	return webTasks(c.ListTasks(ctx, queue, connState(state), page, size, desc))
}

func (s registrySource) GroupTasks(ctx context.Context, name, queue, group string, page, size int) ([]web.Task, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return nil, conn.ErrNotFound
	}
	return webTasks(c.ListGroupTasks(ctx, queue, group, page, size))
}

func webTasks(ts []conn.Task, err error) ([]web.Task, error) {
	if err != nil {
		return nil, webTaskErr(err)
	}
	out := make([]web.Task, len(ts))
	for i, t := range ts {
		out[i] = webTask(t)
	}
	return out, nil
}

func (s registrySource) Groups(ctx context.Context, name, queue string) ([]web.Group, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return nil, conn.ErrNotFound
	}
	gs, err := c.Groups(ctx, queue)
	if err != nil {
		return nil, webTaskErr(err)
	}
	out := make([]web.Group, len(gs))
	for i, g := range gs {
		out[i] = web.Group{Name: g.Name, Size: g.Size}
	}
	return out, nil
}

func (s registrySource) Task(ctx context.Context, name, queue, id string) (web.Task, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return web.Task{}, conn.ErrNotFound
	}
	t, err := c.Task(ctx, queue, id)
	if err != nil {
		return web.Task{}, webTaskErr(err)
	}
	return webTask(t), nil
}

func (s registrySource) TaskAction(ctx context.Context, name, queue string, verb web.TaskVerb, id string) error {
	c, ok := s.reg.Get(name)
	if !ok {
		return conn.ErrNotFound
	}
	var err error
	switch verb {
	case web.TaskRun:
		err = c.RunTask(ctx, queue, id)
	case web.TaskArchive:
		err = c.ArchiveTask(ctx, queue, id)
	case web.TaskDelete:
		err = c.DeleteTask(ctx, queue, id)
	case web.TaskCancel:
		err = c.CancelTask(ctx, queue, id)
	default:
		err = fmt.Errorf("unknown task verb %d", verb)
	}
	return webTaskErr(err)
}

func (s registrySource) TaskActionAll(ctx context.Context, name, queue string, verb web.TaskVerb, state web.TaskState) (int, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return 0, conn.ErrNotFound
	}
	var (
		n   int
		err error
	)
	switch verb {
	case web.TaskRun:
		n, err = c.RunAllTasks(ctx, queue, connState(state))
	case web.TaskArchive:
		n, err = c.ArchiveAllTasks(ctx, queue, connState(state))
	case web.TaskDelete:
		n, err = c.DeleteAllTasks(ctx, queue, connState(state))
	default:
		err = conn.ErrUnsupported
	}
	return n, webTaskErr(err)
}

func (s registrySource) GroupActionAll(ctx context.Context, name, queue string, verb web.TaskVerb, group string) (int, error) {
	c, ok := s.reg.Get(name)
	if !ok {
		return 0, conn.ErrNotFound
	}
	var (
		n   int
		err error
	)
	switch verb {
	case web.TaskRun:
		n, err = c.RunAllGroupTasks(ctx, queue, group)
	case web.TaskArchive:
		n, err = c.ArchiveAllGroupTasks(ctx, queue, group)
	case web.TaskDelete:
		n, err = c.DeleteAllGroupTasks(ctx, queue, group)
	default:
		err = conn.ErrUnsupported
	}
	return n, webTaskErr(err)
}

// webTaskErr adds the web errors a handler tests for to conn's typed
// errors, and marks a read that could not reach the server.
func webTaskErr(err error) error {
	if errors.Is(err, conn.ErrTaskNotFound) {
		return fmt.Errorf("%w: %w", web.ErrTaskNotFound, err)
	}
	return webReadErr(webQueueErr(err))
}

func connState(s web.TaskState) conn.TaskState {
	switch s {
	case web.TaskActive:
		return conn.TaskActive
	case web.TaskPending:
		return conn.TaskPending
	case web.TaskAggregating:
		return conn.TaskAggregating
	case web.TaskScheduled:
		return conn.TaskScheduled
	case web.TaskRetry:
		return conn.TaskRetry
	case web.TaskArchived:
		return conn.TaskArchived
	case web.TaskCompleted:
		return conn.TaskCompleted
	}
	return 0
}

func webState(s conn.TaskState) web.TaskState {
	switch s {
	case conn.TaskActive:
		return web.TaskActive
	case conn.TaskPending:
		return web.TaskPending
	case conn.TaskAggregating:
		return web.TaskAggregating
	case conn.TaskScheduled:
		return web.TaskScheduled
	case conn.TaskRetry:
		return web.TaskRetry
	case conn.TaskArchived:
		return web.TaskArchived
	case conn.TaskCompleted:
		return web.TaskCompleted
	}
	return 0
}

func webTask(t conn.Task) web.Task {
	return web.Task{
		ID:            t.ID,
		Queue:         t.Queue,
		Type:          t.Type,
		Payload:       t.Payload,
		Headers:       t.Headers,
		State:         webState(t.State),
		MaxRetry:      t.MaxRetry,
		Retried:       t.Retried,
		LastErr:       t.LastErr,
		LastFailedAt:  t.LastFailedAt,
		Timeout:       t.Timeout,
		Deadline:      t.Deadline,
		Group:         t.Group,
		NextProcessAt: t.NextProcessAt,
		IsOrphaned:    t.IsOrphaned,
		Retention:     t.Retention,
		CompletedAt:   t.CompletedAt,
		Result:        t.Result,
		Started:       t.Started,
		ArchivedAt:    t.ArchivedAt,
	}
}
