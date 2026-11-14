package web

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// fakeTasks backs fakeConns' TaskSource methods: the tasks of every queue
// of "main", in list order.
type fakeTasks struct {
	mu     sync.Mutex
	tasks  []Task
	groups map[string][]Group
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
	var ts []Task
	for i := range 25 {
		ts = append(ts, Task{ID: fmt.Sprintf("p%02d", i), Queue: "default", Type: "email:welcome", State: TaskPending,
			Payload: []byte(fmt.Sprintf(`{"user_id":%d}`, i)), MaxRetry: 25})
	}
	ts = append(ts,
		Task{ID: "a0", Queue: "default", Type: "email:digest", State: TaskActive, Started: now.Add(-4 * time.Second), Payload: []byte("\x00bin\xff")},
		Task{ID: "a1", Queue: "default", Type: "email:digest", State: TaskActive, IsOrphaned: true},
		Task{ID: "s0", Queue: "default", Type: "report", State: TaskScheduled, NextProcessAt: now.Add(45 * time.Minute)},
		Task{ID: "s1", Queue: "default", Type: "report", State: TaskScheduled, NextProcessAt: now.Add(2 * time.Hour)},
		Task{ID: uuidTask, Queue: "default", Type: "email:receipt", State: TaskRetry, Retried: 3, MaxRetry: 25,
			Payload: []byte(`{"order_id":"ORD-88213","amount":4200}`), Headers: map[string]string{"trace": "t-1", "a": "b"},
			LastErr: "smtp: 450 mailbox unavailable", LastFailedAt: now.Add(-time.Minute), NextProcessAt: now.Add(160 * time.Second),
			Timeout: 30 * time.Second},
		Task{ID: "x0", Queue: "default", Type: "email:receipt", State: TaskArchived, Retried: 25, MaxRetry: 25,
			LastErr: "context deadline exceeded", LastFailedAt: now.Add(-time.Hour), Payload: []byte("<script>alert(1)</script>")},
		Task{ID: "c0", Queue: "default", Type: "email:welcome", State: TaskCompleted, CompletedAt: now.Add(-time.Minute),
			Retention: 24 * time.Hour, Result: []byte(`{"sent":true}`)},
		Task{ID: "low-1", Queue: "low", Type: "cleanup", State: TaskPending},
	)
	for i := range 4 {
		ts = append(ts, Task{ID: fmt.Sprintf("g1-%d", i), Queue: "default", Type: "batch", State: TaskAggregating, Group: "g1"})
	}
	return &fakeTasks{tasks: ts, groups: map[string][]Group{"default": {{Name: "g1", Size: 4}}}, actErr: map[string]error{}}
}

func (f *fakeConns) queueExists(name, queue string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.queues[name], func(q Queue) bool { return q.Name == queue })
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
		return fmt.Errorf("%w: %q", ErrQueueNotFound, queue)
	}
	return nil
}

func (f *fakeConns) QueueInfo(ctx context.Context, name, queue string) (QueueInfo, error) {
	if err := f.read(ctx, name, queue); err != nil {
		return QueueInfo{}, err
	}
	f.mu.Lock()
	var qi QueueInfo
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
		case TaskPending:
			qi.Pending++
		case TaskActive:
			qi.Active++
		case TaskScheduled:
			qi.Scheduled++
		case TaskRetry:
			qi.Retry++
		case TaskArchived:
			qi.Archived++
		case TaskCompleted:
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
func (f *fakeConns) Tasks(ctx context.Context, name, queue string, state TaskState, page, size int, desc bool) ([]Task, error) {
	return f.page(ctx, name, queue, page, size, desc, func(t Task) bool { return t.State == state })
}

func (f *fakeConns) GroupTasks(ctx context.Context, name, queue, group string, page, size int) ([]Task, error) {
	return f.page(ctx, name, queue, page, size, false, func(t Task) bool { return t.State == TaskAggregating && t.Group == group })
}

func (f *fakeConns) page(ctx context.Context, name, queue string, page, size int, desc bool, keep func(Task) bool) ([]Task, error) {
	if err := f.read(ctx, name, queue); err != nil {
		return nil, err
	}
	t := f.tasks
	t.mu.Lock()
	defer t.mu.Unlock()
	var all []Task
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

func (f *fakeConns) Groups(ctx context.Context, name, queue string) ([]Group, error) {
	if err := f.read(ctx, name, queue); err != nil {
		return nil, err
	}
	f.tasks.mu.Lock()
	defer f.tasks.mu.Unlock()
	return f.tasks.groups[queue], nil
}

func (f *fakeConns) Task(ctx context.Context, name, queue, id string) (Task, error) {
	if err := f.read(ctx, name, queue); err != nil {
		return Task{}, err
	}
	f.tasks.mu.Lock()
	defer f.tasks.mu.Unlock()
	for _, task := range f.tasks.tasks {
		if task.Queue == queue && task.ID == id {
			return task, nil
		}
	}
	return Task{}, fmt.Errorf("%w: %s", ErrTaskNotFound, id)
}

func (f *fakeConns) TaskAction(_ context.Context, _, queue string, verb TaskVerb, id string) error {
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
		if task.State == TaskActive && verb != TaskCancel {
			return errors.New("asynq: task is already running")
		}
		switch verb {
		case TaskRun:
			t.tasks[i].State = TaskPending
		case TaskArchive:
			t.tasks[i].State = TaskArchived
		case TaskDelete:
			t.tasks = slices.Delete(t.tasks, i, i+1)
		case TaskCancel:
			if task.State != TaskActive {
				return errors.New("conn: action does not apply to the task state")
			}
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrTaskNotFound, id)
}

func (f *fakeConns) TaskActionAll(_ context.Context, _, queue string, verb TaskVerb, state TaskState) (int, error) {
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
			case TaskRun:
				task.State = TaskPending
			case TaskArchive:
				task.State = TaskArchived
			case TaskDelete:
				continue
			}
		}
		kept = append(kept, task)
	}
	t.tasks = kept
	return n, nil
}

func (f *fakeConns) GroupActionAll(_ context.Context, _, queue string, verb TaskVerb, group string) (int, error) {
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
		if task.Queue == queue && task.State == TaskAggregating && task.Group == group {
			n++
			switch verb {
			case TaskRun:
				task.State = TaskPending
			case TaskArchive:
				task.State = TaskArchived
			case TaskDelete:
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

func (f *fakeConns) taskActions() []string {
	f.tasks.mu.Lock()
	defer f.tasks.mu.Unlock()
	return append([]string(nil), f.tasks.acted...)
}
