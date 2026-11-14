package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/BarkinBalci/asynqui/internal/conn"
)

// fakeTasks backs fakeConns' TaskSource methods: the tasks of every queue
// of "main", in list order.
type fakeTasks struct {
	mu     sync.Mutex
	tasks  []conn.Task
	groups map[string][]conn.Group
	// block makes the reads wait until ctx ends.
	block bool
	// readErr fails the reads; actErr fails actions on a task ID ("" for
	// the "all" actions).
	readErr error
	actErr  map[string]error
	// acted records "verb queue/id" and "verb-all queue/state".
	acted []string
}

// uuidTask is a retry task with a UUID ID, a JSON payload, headers and an
// error.
const uuidTask = "c3e81b90-04aa-4f1e-9d27-6b52e0c8a113"

func defaultTasks() *fakeTasks {
	now := fixedNow()
	var ts []conn.Task
	for i := range 25 {
		ts = append(ts, conn.Task{ID: fmt.Sprintf("p%02d", i), Queue: "default", Type: "email:welcome", State: conn.TaskPending,
			Payload: []byte(fmt.Sprintf(`{"user_id":%d}`, i)), MaxRetry: 25})
	}
	ts = append(ts,
		conn.Task{ID: "a0", Queue: "default", Type: "email:digest", State: conn.TaskActive, Started: now.Add(-4 * time.Second), Payload: []byte("\x00bin\xff")},
		conn.Task{ID: "a1", Queue: "default", Type: "email:digest", State: conn.TaskActive, IsOrphaned: true},
		conn.Task{ID: "s0", Queue: "default", Type: "report", State: conn.TaskScheduled, NextProcessAt: now.Add(45 * time.Minute)},
		conn.Task{ID: "s1", Queue: "default", Type: "report", State: conn.TaskScheduled, NextProcessAt: now.Add(2 * time.Hour)},
		conn.Task{ID: uuidTask, Queue: "default", Type: "email:receipt", State: conn.TaskRetry, Retried: 3, MaxRetry: 25,
			Payload: []byte(`{"order_id":"ORD-88213","amount":4200}`), Headers: map[string]string{"trace": "t-1", "a": "b"},
			LastErr: "smtp: 450 mailbox unavailable", LastFailedAt: now.Add(-time.Minute), NextProcessAt: now.Add(160 * time.Second),
			Timeout: 30 * time.Second},
		conn.Task{ID: "x0", Queue: "default", Type: "email:receipt", State: conn.TaskArchived, Retried: 25, MaxRetry: 25,
			LastErr: "context deadline exceeded", LastFailedAt: now.Add(-time.Hour), Payload: []byte("<script>alert(1)</script>")},
		conn.Task{ID: "c0", Queue: "default", Type: "email:welcome", State: conn.TaskCompleted, CompletedAt: now.Add(-time.Minute),
			Retention: 24 * time.Hour, Result: []byte(`{"sent":true}`)},
		conn.Task{ID: "low-1", Queue: "low", Type: "cleanup", State: conn.TaskPending},
	)
	for i := range 4 {
		ts = append(ts, conn.Task{ID: fmt.Sprintf("g1-%d", i), Queue: "default", Type: "batch", State: conn.TaskAggregating, Group: "g1"})
	}
	return &fakeTasks{tasks: ts, groups: map[string][]conn.Group{"default": {{Name: "g1", Size: 4}}}, actErr: map[string]error{}}
}

func (f *fakeConns) queueExists(name, queue string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.queues[name], func(q conn.Queue) bool { return q.Name == queue })
}

func (f *fakeConns) read(ctx context.Context, name, queue string) error {
	t := f.tasks
	t.mu.Lock()
	block, err := t.block, t.readErr
	t.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if !f.queueExists(name, queue) {
		return fmt.Errorf("%w: %q", conn.ErrQueueNotFound, queue)
	}
	return nil
}

func (c fakeConn) QueueInfo(ctx context.Context, queue string) (conn.QueueInfo, error) {
	f, name := c.f, c.name
	if err := f.read(ctx, name, queue); err != nil {
		return conn.QueueInfo{}, err
	}
	f.mu.Lock()
	var qi conn.QueueInfo
	for _, q := range f.queues[name] {
		if q.Name == queue {
			qi.Queue = q
		}
	}
	f.mu.Unlock()
	qi.Pending, qi.Active, qi.Scheduled, qi.Retry, qi.Archived = 0, 0, 0, 0, 0
	t := f.tasks
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, task := range t.tasks {
		if task.Queue != queue {
			continue
		}
		switch task.State {
		case conn.TaskPending:
			qi.Pending++
		case conn.TaskActive:
			qi.Active++
		case conn.TaskScheduled:
			qi.Scheduled++
		case conn.TaskRetry:
			qi.Retry++
		case conn.TaskArchived:
			qi.Archived++
		case conn.TaskCompleted:
			qi.Completed++
		}
	}
	for _, g := range t.groups[queue] {
		qi.Aggregating += g.Size
	}
	return qi, nil
}

// Tasks pages over the state's tasks in list order (oldest first), or
// reversed with desc.
func (c fakeConn) ListTasks(ctx context.Context, queue string, state conn.TaskState, page, size int, desc bool) ([]conn.Task, error) {
	f, name := c.f, c.name
	return f.page(ctx, name, queue, page, size, desc, func(t conn.Task) bool { return t.State == state })
}

func (c fakeConn) ListGroupTasks(ctx context.Context, queue, group string, page, size int) ([]conn.Task, error) {
	f, name := c.f, c.name
	return f.page(ctx, name, queue, page, size, false, func(t conn.Task) bool { return t.State == conn.TaskAggregating && t.Group == group })
}

func (f *fakeConns) page(ctx context.Context, name, queue string, page, size int, desc bool, keep func(conn.Task) bool) ([]conn.Task, error) {
	if err := f.read(ctx, name, queue); err != nil {
		return nil, err
	}
	t := f.tasks
	t.mu.Lock()
	defer t.mu.Unlock()
	var all []conn.Task
	for _, task := range t.tasks {
		if task.Queue == queue && keep(task) {
			all = append(all, task)
		}
	}
	if desc {
		slices.Reverse(all)
	}
	start := (page - 1) * size
	if start >= len(all) {
		return nil, nil
	}
	return all[start:min(start+size, len(all))], nil
}

func (c fakeConn) Groups(ctx context.Context, queue string) ([]conn.Group, error) {
	f, name := c.f, c.name
	if err := f.read(ctx, name, queue); err != nil {
		return nil, err
	}
	f.tasks.mu.Lock()
	defer f.tasks.mu.Unlock()
	return f.tasks.groups[queue], nil
}

func (c fakeConn) Task(ctx context.Context, queue, id string) (conn.Task, error) {
	f, name := c.f, c.name
	if err := f.read(ctx, name, queue); err != nil {
		return conn.Task{}, err
	}
	f.tasks.mu.Lock()
	defer f.tasks.mu.Unlock()
	for _, task := range f.tasks.tasks {
		if task.Queue == queue && task.ID == id {
			return task, nil
		}
	}
	return conn.Task{}, fmt.Errorf("%w: %s", conn.ErrTaskNotFound, id)
}

func (f *fakeConns) taskAction(queue string, verb conn.TaskVerb, id string) error {
	t := f.tasks
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acted = append(t.acted, verbParam(verb)+" "+queue+"/"+id)
	if err := t.actErr[id]; err != nil {
		return err
	}
	for i, task := range t.tasks {
		if task.Queue != queue || task.ID != id {
			continue
		}
		if task.State == conn.TaskActive && verb != conn.TaskCancel {
			return errors.New("asynq: task is already running")
		}
		switch verb {
		case conn.TaskRun:
			t.tasks[i].State = conn.TaskPending
		case conn.TaskArchive:
			t.tasks[i].State = conn.TaskArchived
		case conn.TaskDelete:
			t.tasks = slices.Delete(t.tasks, i, i+1)
		case conn.TaskCancel:
			if task.State != conn.TaskActive {
				return errors.New("conn: action does not apply to the task state")
			}
		}
		return nil
	}
	return fmt.Errorf("%w: %s", conn.ErrTaskNotFound, id)
}

func (f *fakeConns) allAction(queue string, verb conn.TaskVerb, state conn.TaskState) (int, error) {
	t := f.tasks
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acted = append(t.acted, verbParam(verb)+"-all "+queue+"/"+stateParam(state))
	if err := t.actErr[""]; err != nil {
		return 0, err
	}
	n := 0
	kept := t.tasks[:0]
	for _, task := range t.tasks {
		if task.Queue == queue && task.State == state {
			n++
			switch verb {
			case conn.TaskRun:
				task.State = conn.TaskPending
			case conn.TaskArchive:
				task.State = conn.TaskArchived
			case conn.TaskDelete:
				continue
			}
		}
		kept = append(kept, task)
	}
	t.tasks = kept
	return n, nil
}

func (f *fakeConns) groupAction(queue string, verb conn.TaskVerb, group string) (int, error) {
	t := f.tasks
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acted = append(t.acted, verbParam(verb)+"-all "+queue+"/group:"+group)
	if err := t.actErr["group:"+group]; err != nil {
		return 0, err
	}
	n := 0
	kept := t.tasks[:0]
	for _, task := range t.tasks {
		if task.Queue == queue && task.State == conn.TaskAggregating && task.Group == group {
			n++
			switch verb {
			case conn.TaskRun:
				task.State = conn.TaskPending
			case conn.TaskArchive:
				task.State = conn.TaskArchived
			case conn.TaskDelete:
				continue
			}
		}
		kept = append(kept, task)
	}
	t.tasks = kept
	gs := t.groups[queue]
	for i := range gs {
		if gs[i].Name == group {
			gs[i].Size = 0
		}
	}
	return n, nil
}

func (c fakeConn) TaskAction(_ context.Context, q string, verb conn.TaskVerb, id string) error {
	return c.f.taskAction(q, verb, id)
}

func (c fakeConn) AllAction(_ context.Context, q string, verb conn.TaskVerb, s conn.TaskState) (int, error) {
	return c.f.allAction(q, verb, s)
}

func (c fakeConn) GroupAction(_ context.Context, q, g string, verb conn.TaskVerb) (int, error) {
	return c.f.groupAction(q, verb, g)
}

func (f *fakeConns) taskActions() []string {
	f.tasks.mu.Lock()
	defer f.tasks.mu.Unlock()
	return append([]string(nil), f.tasks.acted...)
}
