package conn

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// fakeTasks is a hand-written taskInspector. Tasks live in one map by
// queue and ID; the list methods page over the IDs in order.
type fakeTasks struct {
	mu     sync.Mutex
	tasks  map[string]*asynq.TaskInfo // by queue + "/" + id
	order  []string                   // keys in list order
	groups map[string][]*asynq.GroupInfo
	// err fails every call when set; taskErr fails GetTaskInfo of one
	// task, and delay slows GetTaskInfo of the others.
	err     error
	taskErr map[string]error
	delay   time.Duration
	// calls counts the calls by method name; opts records the last list
	// call's page and size.
	calls map[string]int
	page  [2]int
}

func newFakeTasks(tasks ...*asynq.TaskInfo) *fakeTasks {
	f := &fakeTasks{tasks: map[string]*asynq.TaskInfo{}, calls: map[string]int{}, groups: map[string][]*asynq.GroupInfo{}}
	for _, t := range tasks {
		k := t.Queue + "/" + t.ID
		f.tasks[k] = t
		f.order = append(f.order, k)
	}
	return f
}

func (f *fakeTasks) call(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
	return f.err
}

func (f *fakeTasks) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

func (f *fakeTasks) GetTaskInfo(queue, id string) (*asynq.TaskInfo, error) {
	if err := f.call("GetTaskInfo"); err != nil {
		return nil, err
	}
	if queue == "gone" {
		return nil, fmt.Errorf("asynq: %w", asynq.ErrQueueNotFound)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.taskErr[id]; err != nil {
		return nil, err
	}
	if f.delay > 0 {
		f.mu.Unlock()
		time.Sleep(f.delay)
		f.mu.Lock()
	}
	t, ok := f.tasks[queue+"/"+id]
	if !ok {
		return nil, fmt.Errorf("asynq: %w", asynq.ErrTaskNotFound)
	}
	return t, nil
}

// list pages over the tasks of queue in state. Options are read through
// asynq's own composition: PageSize and Page are opaque, so the fake
// takes them from the call's arguments by position.
func (f *fakeTasks) list(name, queue string, state asynq.TaskState, opts []asynq.ListOption) ([]*asynq.TaskInfo, error) {
	if err := f.call(name); err != nil {
		return nil, err
	}
	if queue == "gone" {
		return nil, fmt.Errorf("asynq: %w", asynq.ErrQueueNotFound)
	}
	size, page := 30, 1
	for i, o := range opts {
		if fmt.Sprint(o) == "" {
			continue
		}
		var n int
		_, _ = fmt.Sscan(fmt.Sprint(o), &n)
		if i == 0 {
			size = n
		} else {
			page = n
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.page = [2]int{page, size}
	var all []*asynq.TaskInfo
	for _, k := range f.order {
		if t := f.tasks[k]; t != nil && t.Queue == queue && t.State == state {
			all = append(all, t)
		}
	}
	start := (page - 1) * size
	if start >= len(all) {
		return nil, nil
	}
	return all[start:min(start+size, len(all))], nil
}

func (f *fakeTasks) ListPendingTasks(q string, o ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("ListPendingTasks", q, asynq.TaskStatePending, o)
}

func (f *fakeTasks) ListActiveTasks(q string, o ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("ListActiveTasks", q, asynq.TaskStateActive, o)
}

func (f *fakeTasks) ListScheduledTasks(q string, o ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("ListScheduledTasks", q, asynq.TaskStateScheduled, o)
}

func (f *fakeTasks) ListRetryTasks(q string, o ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("ListRetryTasks", q, asynq.TaskStateRetry, o)
}

func (f *fakeTasks) ListArchivedTasks(q string, o ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("ListArchivedTasks", q, asynq.TaskStateArchived, o)
}

func (f *fakeTasks) ListCompletedTasks(q string, o ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	return f.list("ListCompletedTasks", q, asynq.TaskStateCompleted, o)
}

func (f *fakeTasks) ListAggregatingTasks(q, g string, o ...asynq.ListOption) ([]*asynq.TaskInfo, error) {
	all, err := f.list("ListAggregatingTasks", q, asynq.TaskStateAggregating, o)
	var out []*asynq.TaskInfo
	for _, t := range all {
		if t.Group == g {
			out = append(out, t)
		}
	}
	return out, err
}

func (f *fakeTasks) groupAll(name, queue, group string, to asynq.TaskState, del bool) (int, error) {
	if err := f.call(name); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, t := range f.tasks {
		if t.Queue != queue || t.State != asynq.TaskStateAggregating || t.Group != group {
			continue
		}
		n++
		if del {
			delete(f.tasks, k)
		} else {
			t.State = to
		}
	}
	return n, nil
}

func (f *fakeTasks) RunAllAggregatingTasks(q, g string) (int, error) {
	return f.groupAll("RunAllAggregatingTasks", q, g, asynq.TaskStatePending, false)
}

func (f *fakeTasks) ArchiveAllAggregatingTasks(q, g string) (int, error) {
	return f.groupAll("ArchiveAllAggregatingTasks", q, g, asynq.TaskStateArchived, false)
}

func (f *fakeTasks) DeleteAllAggregatingTasks(q, g string) (int, error) {
	return f.groupAll("DeleteAllAggregatingTasks", q, g, 0, true)
}

func (f *fakeTasks) Groups(q string) ([]*asynq.GroupInfo, error) {
	if err := f.call("Groups"); err != nil {
		return nil, err
	}
	return f.groups[q], nil
}

func (f *fakeTasks) act(name, queue, id string, to asynq.TaskState, del bool) error {
	if err := f.call(name); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[queue+"/"+id]
	if !ok {
		return fmt.Errorf("asynq: %w", asynq.ErrTaskNotFound)
	}
	if t.State == asynq.TaskStateActive {
		return errors.New("asynq: task is already running")
	}
	if del {
		delete(f.tasks, queue+"/"+id)
		return nil
	}
	t.State = to
	return nil
}

func (f *fakeTasks) RunTask(q, id string) error {
	return f.act("RunTask", q, id, asynq.TaskStatePending, false)
}

func (f *fakeTasks) ArchiveTask(q, id string) error {
	return f.act("ArchiveTask", q, id, asynq.TaskStateArchived, false)
}

func (f *fakeTasks) DeleteTask(q, id string) error { return f.act("DeleteTask", q, id, 0, true) }

func (f *fakeTasks) CancelProcessing(string) error { return f.call("CancelProcessing") }

func (f *fakeTasks) all(name, queue string, from, to asynq.TaskState, del bool) (int, error) {
	if err := f.call(name); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for k, t := range f.tasks {
		if t.Queue != queue || t.State != from {
			continue
		}
		n++
		if del {
			delete(f.tasks, k)
		} else {
			t.State = to
		}
	}
	return n, nil
}

func (f *fakeTasks) RunAllScheduledTasks(q string) (int, error) {
	return f.all("RunAllScheduledTasks", q, asynq.TaskStateScheduled, asynq.TaskStatePending, false)
}

func (f *fakeTasks) RunAllRetryTasks(q string) (int, error) {
	return f.all("RunAllRetryTasks", q, asynq.TaskStateRetry, asynq.TaskStatePending, false)
}

func (f *fakeTasks) RunAllArchivedTasks(q string) (int, error) {
	return f.all("RunAllArchivedTasks", q, asynq.TaskStateArchived, asynq.TaskStatePending, false)
}

func (f *fakeTasks) ArchiveAllPendingTasks(q string) (int, error) {
	return f.all("ArchiveAllPendingTasks", q, asynq.TaskStatePending, asynq.TaskStateArchived, false)
}

func (f *fakeTasks) ArchiveAllScheduledTasks(q string) (int, error) {
	return f.all("ArchiveAllScheduledTasks", q, asynq.TaskStateScheduled, asynq.TaskStateArchived, false)
}

func (f *fakeTasks) ArchiveAllRetryTasks(q string) (int, error) {
	return f.all("ArchiveAllRetryTasks", q, asynq.TaskStateRetry, asynq.TaskStateArchived, false)
}

func (f *fakeTasks) DeleteAllPendingTasks(q string) (int, error) {
	return f.all("DeleteAllPendingTasks", q, asynq.TaskStatePending, 0, true)
}

func (f *fakeTasks) DeleteAllScheduledTasks(q string) (int, error) {
	return f.all("DeleteAllScheduledTasks", q, asynq.TaskStateScheduled, 0, true)
}

func (f *fakeTasks) DeleteAllRetryTasks(q string) (int, error) {
	return f.all("DeleteAllRetryTasks", q, asynq.TaskStateRetry, 0, true)
}

func (f *fakeTasks) DeleteAllArchivedTasks(q string) (int, error) {
	return f.all("DeleteAllArchivedTasks", q, asynq.TaskStateArchived, 0, true)
}

func (f *fakeTasks) DeleteAllCompletedTasks(q string) (int, error) {
	return f.all("DeleteAllCompletedTasks", q, asynq.TaskStateCompleted, 0, true)
}

func taskConn(t *testing.T, f *fakeTasks, queues ...string) *Conn {
	t.Helper()
	c := testConn(t, "main", nil)
	c.tasks = f
	c.keys = keysOf(f)
	infos := map[string]*asynq.QueueInfo{}
	for _, q := range queues {
		infos[q] = &asynq.QueueInfo{Queue: q, Pending: 2, Completed: 4, Aggregating: 5}
	}
	c.queues = &fakeQueues{names: queues, infos: infos}
	return c
}

// keysOf lays out the fake's tasks as asynq would: each scheduled, retry,
// archived or completed task in its state's sorted set, scored by its
// position.
func keysOf(f *fakeTasks) *fakeKeys {
	k := newFakeKeys()
	for i, key := range f.order {
		t := f.tasks[key]
		if name, ok := stateKey(t.Queue, taskState(t.State)); ok {
			k.zsets[name] = append(k.zsets[name], redis.Z{Score: float64(i), Member: t.ID})
		}
	}
	return k
}

func ti(queue, id string, state asynq.TaskState) *asynq.TaskInfo {
	return &asynq.TaskInfo{ID: id, Queue: queue, Type: "email:welcome", State: state, Payload: []byte(`{"a":1}`)}
}

func TestQueueInfo(t *testing.T) {
	c := taskConn(t, newFakeTasks(), "emails")
	got, err := c.QueueInfo(t.Context(), "emails")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "emails" || got.Pending != 2 || got.Completed != 4 || got.Aggregating != 5 {
		t.Errorf("QueueInfo = %+v", got)
	}
	if _, err := c.QueueInfo(t.Context(), "nope"); !errors.Is(err, ErrQueueNotFound) {
		t.Errorf("unknown queue: err = %v, want ErrQueueNotFound", err)
	}
}

func TestListTasks(t *testing.T) {
	var tasks []*asynq.TaskInfo
	for i := range 5 {
		tasks = append(tasks, ti("emails", fmt.Sprintf("p%d", i), asynq.TaskStatePending))
	}
	tasks = append(tasks, ti("emails", "s0", asynq.TaskStateScheduled), ti("other", "p9", asynq.TaskStatePending))
	f := newFakeTasks(tasks...)
	c := taskConn(t, f, "emails", "other")
	keys := c.keys.(*fakeKeys)

	ids := func(ts []Task) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.ID)
		}
		return out
	}
	got, err := c.ListTasks(t.Context(), "emails", TaskPending, 2, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"p2", "p3"}, ids(got)); diff != "" {
		t.Errorf("page 2 (-want +got):\n%s", diff)
	}
	if got[0].State != TaskPending || got[0].Type != "email:welcome" || string(got[0].Payload) != `{"a":1}` {
		t.Errorf("task = %+v", got[0])
	}
	if f.page != [2]int{2, 2} {
		t.Errorf("asked page/size %v, want [2 2]", f.page)
	}
	// Pending comes from the Inspector, in its order: desc is ignored.
	got, err = c.ListTasks(t.Context(), "emails", TaskPending, 1, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"p0", "p1"}, ids(got)); diff != "" {
		t.Errorf("pending with desc (-want +got):\n%s", diff)
	}
	if keys.reads != 0 {
		t.Errorf("pending read asynq's keys %d times", keys.reads)
	}

	// Cached: the same page within a second is one read.
	if _, err := c.ListTasks(t.Context(), "emails", TaskScheduled, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	reads, infos := keys.reads, f.count("GetTaskInfo")
	if _, err := c.ListTasks(t.Context(), "emails", TaskScheduled, 1, 2, false); err != nil {
		t.Fatal(err)
	}
	if keys.reads != reads || f.count("GetTaskInfo") != infos {
		t.Errorf("a cached page read Redis again")
	}

	// Page size is bounded.
	if _, err := c.ListTasks(t.Context(), "emails", TaskScheduled, 0, 1000, false); err != nil {
		t.Fatal(err)
	}
	if keys.last.key != "asynq:{emails}:scheduled" || keys.last.start != 0 || keys.last.stop != MaxPageSize-1 {
		t.Errorf("read %+v, want ranks 0..%d of the scheduled set", keys.last, MaxPageSize-1)
	}

	if _, err := c.ListTasks(t.Context(), "emails", TaskAggregating, 1, 20, false); !errors.Is(err, ErrUnsupported) {
		t.Errorf("aggregating: err = %v, want ErrUnsupported", err)
	}
	if _, err := c.ListTasks(t.Context(), "gone", TaskRetry, 1, 20, false); !errors.Is(err, ErrQueueNotFound) {
		t.Errorf("missing queue: err = %v, want ErrQueueNotFound", err)
	}
	if _, err := c.ListTasks(t.Context(), "gone", TaskPending, 1, 20, false); !errors.Is(err, ErrQueueNotFound) || !errors.Is(err, asynq.ErrQueueNotFound) {
		t.Errorf("missing queue, pending: err = %v, want ErrQueueNotFound", err)
	}
	if _, err := c.ListTasks(t.Context(), "gone", TaskActive, 1, 20, false); !errors.Is(err, ErrQueueNotFound) || !errors.Is(err, asynq.ErrQueueNotFound) {
		t.Errorf("missing queue, active: err = %v, want ErrQueueNotFound", err)
	}
}

// The sorted states read a page of IDs with their scores in either
// direction, hydrate each, and leave out the IDs that vanished or moved
// on between the two reads.
func TestListTasksSorted(t *testing.T) {
	f := newFakeTasks(
		ti("emails", "x0", asynq.TaskStateArchived), ti("emails", "x1", asynq.TaskStateArchived),
		ti("emails", "x2", asynq.TaskStatePending), ti("emails", "x3", asynq.TaskStateArchived),
	)
	c := taskConn(t, f, "emails")
	keys := newFakeKeys()
	keys.zsets["asynq:{emails}:archived"] = []redis.Z{
		{Score: 100, Member: "x0"}, {Score: 200, Member: "ghost"}, {Score: 300, Member: "x1"},
		{Score: 400, Member: "x2"}, {Score: 500, Member: "x3"},
	}
	c.keys = keys

	got, err := c.ListTasks(t.Context(), "emails", TaskArchived, 1, 20, true)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, task := range got {
		ids = append(ids, task.ID+"@"+strconv.FormatInt(task.ArchivedAt.Unix(), 10))
	}
	if diff := cmp.Diff([]string{"x3@500", "x1@300", "x0@100"}, ids); diff != "" {
		t.Errorf("archived newest first (-want +got):\n%s", diff)
	}
	if !keys.last.rev {
		t.Error("newest first read without REV")
	}

	keys.err = errors.New("LOADING")
	if _, err := c.ListTasks(t.Context(), "emails", TaskArchived, 2, 20, false); !errors.Is(err, keys.err) {
		t.Errorf("a failing range read: err = %v", err)
	}
}

// A task whose read fails with an error reply is left out of the page;
// a gone queue or an unreachable server fails the page, and no further
// reads start after the first such failure.
func TestListTasksHydrateErrors(t *testing.T) {
	var tasks []*asynq.TaskInfo
	var zs []redis.Z
	for i := range 40 {
		id := fmt.Sprintf("r%02d", i)
		tasks = append(tasks, ti("emails", id, asynq.TaskStateRetry))
		zs = append(zs, redis.Z{Score: float64(i), Member: id})
	}
	tests := []struct {
		name    string
		err     error
		want    error // nil: the page loads without the task
		maxRead int
	}{
		{"error reply skips the task", replyErr("NOPERM no permissions"), nil, 40},
		{"queue gone fails the page", fmt.Errorf("asynq: %w", asynq.ErrQueueNotFound), ErrQueueNotFound, 1 + queueConcurrency},
		{"unreachable fails the page", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}, nil, 1 + queueConcurrency},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeTasks(tasks...)
			f.taskErr = map[string]error{"r00": tt.err}
			f.delay = 5 * time.Millisecond
			c := taskConn(t, f, "emails")
			keys := newFakeKeys()
			keys.zsets["asynq:{emails}:retry"] = zs
			c.keys = keys
			got, err := c.ListTasks(t.Context(), "emails", TaskRetry, 1, 40, false)
			unreachable := Unreachable(tt.err)
			switch {
			case unreachable:
				if !Unreachable(err) {
					t.Errorf("err = %v, want an unreachable error", err)
				}
			case tt.want != nil:
				if !errors.Is(err, tt.want) {
					t.Errorf("err = %v, want %v", err, tt.want)
				}
			default:
				if err != nil || len(got) != 39 || got[0].ID != "r01" {
					t.Errorf("page = %d tasks, %v; want 39 without r00", len(got), err)
				}
			}
			if n := f.count("GetTaskInfo"); n > tt.maxRead {
				t.Errorf("%d reads, want at most %d", n, tt.maxRead)
			}
		})
	}
}

func TestListActiveTasksStarted(t *testing.T) {
	started := time.Date(2026, 11, 8, 0, 14, 2, 0, time.UTC)
	f := newFakeTasks(ti("emails", "a1", asynq.TaskStateActive), ti("emails", "a2", asynq.TaskStateActive))
	c := taskConn(t, f, "emails")
	c.servers = &fakeServers{infos: []*asynq.ServerInfo{{ActiveWorkers: []*asynq.WorkerInfo{{TaskID: "a1", Started: started}}}, nil}}
	got, err := c.ListTasks(t.Context(), "emails", TaskActive, 1, 20, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Started.Equal(started) || !got[1].Started.IsZero() {
		t.Errorf("active = %+v", got)
	}
}

func TestTask(t *testing.T) {
	info := ti("emails", "c3e81b90", asynq.TaskStateRetry)
	info.Headers = map[string]string{"trace": "x"}
	info.Retried, info.MaxRetry, info.LastErr = 3, 25, "smtp: 450"
	f := newFakeTasks(info)
	c := taskConn(t, f, "emails")

	got, err := c.Task(t.Context(), "emails", "c3e81b90")
	if err != nil {
		t.Fatal(err)
	}
	want := Task{ID: "c3e81b90", Queue: "emails", Type: "email:welcome", Payload: []byte(`{"a":1}`),
		Headers: map[string]string{"trace": "x"}, State: TaskRetry, MaxRetry: 25, Retried: 3, LastErr: "smtp: 450"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Task (-want +got):\n%s", diff)
	}

	tests := []struct {
		queue, id string
		want      []error
	}{
		{"emails", "nope", []error{ErrTaskNotFound, asynq.ErrTaskNotFound}},
		{"gone", "c3e81b90", []error{ErrQueueNotFound, asynq.ErrQueueNotFound}},
		{"emails", "  ", []error{ErrTaskNotFound}},
	}
	for _, tt := range tests {
		_, err := c.Task(t.Context(), tt.queue, tt.id)
		for _, w := range tt.want {
			if !errors.Is(err, w) {
				t.Errorf("Task(%q, %q): err = %v, want %v", tt.queue, tt.id, err, w)
			}
		}
	}
}

func TestTaskActions(t *testing.T) {
	f := newFakeTasks(ti("emails", "r1", asynq.TaskStateRetry), ti("emails", "a1", asynq.TaskStateActive), ti("emails", "p1", asynq.TaskStatePending))
	c := taskConn(t, f, "emails")

	// Fill the cache, act, and the next read is fresh.
	if _, err := c.Task(t.Context(), "emails", "r1"); err != nil {
		t.Fatal(err)
	}
	if err := c.RunTask(t.Context(), "emails", "r1"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Task(t.Context(), "emails", "r1")
	if err != nil || got.State != TaskPending {
		t.Errorf("after run: %+v, %v; want pending (cache dropped)", got, err)
	}
	if err := c.ArchiveTask(t.Context(), "emails", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTask(t.Context(), "emails", "p1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTask(t.Context(), "emails", "p1"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("delete twice: err = %v, want ErrTaskNotFound", err)
	}
	if err := c.DeleteTask(t.Context(), "emails", "a1"); err == nil || errors.Is(err, ErrTaskNotFound) {
		t.Errorf("delete active: err = %v, want asynq's state error", err)
	}

	// Cancel: only active tasks of this queue get the signal.
	if err := c.CancelTask(t.Context(), "emails", "a1"); err != nil {
		t.Fatal(err)
	}
	if err := c.CancelTask(t.Context(), "emails", "r1"); !errors.Is(err, ErrTaskState) {
		t.Errorf("cancel pending: err = %v, want ErrTaskState", err)
	}
	if err := c.CancelTask(t.Context(), "emails", "zz"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("cancel unknown: err = %v, want ErrTaskNotFound", err)
	}
	if n := f.count("CancelProcessing"); n != 1 {
		t.Errorf("%d cancel signals, want 1", n)
	}
}

func TestAllActions(t *testing.T) {
	f := newFakeTasks(ti("emails", "s1", asynq.TaskStateScheduled), ti("emails", "s2", asynq.TaskStateScheduled),
		ti("emails", "c1", asynq.TaskStateCompleted), ti("emails", "a1", asynq.TaskStateActive))
	c := taskConn(t, f, "emails")

	n, err := c.RunAllTasks(t.Context(), "emails", TaskScheduled)
	if err != nil || n != 2 {
		t.Errorf("RunAll scheduled = %d, %v; want 2", n, err)
	}
	n, err = c.DeleteAllTasks(t.Context(), "emails", TaskCompleted)
	if err != nil || n != 1 {
		t.Errorf("DeleteAll completed = %d, %v; want 1", n, err)
	}
	n, err = c.ArchiveAllTasks(t.Context(), "emails", TaskPending)
	if err != nil || n != 2 {
		t.Errorf("ArchiveAll pending = %d, %v; want 2 (the run ones)", n, err)
	}

	unsupported := []struct {
		name string
		fn   func() (int, error)
	}{
		{"run all pending", func() (int, error) { return c.RunAllTasks(t.Context(), "emails", TaskPending) }},
		{"run all completed", func() (int, error) { return c.RunAllTasks(t.Context(), "emails", TaskCompleted) }},
		{"archive all archived", func() (int, error) { return c.ArchiveAllTasks(t.Context(), "emails", TaskArchived) }},
		{"archive all completed", func() (int, error) { return c.ArchiveAllTasks(t.Context(), "emails", TaskCompleted) }},
		{"any all active", func() (int, error) { return c.DeleteAllTasks(t.Context(), "emails", TaskActive) }},
		{"delete all aggregating", func() (int, error) { return c.DeleteAllTasks(t.Context(), "emails", TaskAggregating) }},
	}
	for _, u := range unsupported {
		if _, err := u.fn(); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: err = %v, want ErrUnsupported", u.name, err)
		}
	}
	if _, err := c.DeleteAllTasks(t.Context(), "nope", TaskPending); !errors.Is(err, ErrQueueNotFound) {
		t.Errorf("unknown queue: err = %v, want ErrQueueNotFound", err)
	}
	if f.count("DeleteAllPendingTasks") != 0 {
		t.Error("an all action ran on an unknown queue")
	}
}

func TestGroups(t *testing.T) {
	f := newFakeTasks()
	f.groups["emails"] = []*asynq.GroupInfo{{Group: "b", Size: 2}, nil, {Group: "a", Size: 7}}
	c := taskConn(t, f, "emails")
	got, err := c.Groups(t.Context(), "emails")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]Group{{"a", 7}, {"b", 2}}, got); diff != "" {
		t.Errorf("Groups (-want +got):\n%s", diff)
	}
	if _, err := c.Groups(t.Context(), "nope"); !errors.Is(err, ErrQueueNotFound) {
		t.Errorf("unknown queue: err = %v", err)
	}
}

// Keys of different queues and pages never collide, whatever the names.
func TestTaskCacheKeys(t *testing.T) {
	keys := []string{
		string(tasksKey("a/1", TaskPending, 1, 20, false)), string(tasksKey("a", TaskPending, 1, 20, false)),
		string(tasksKey("a", TaskPending, 12, 0, false)), string(tasksKey("a", TaskPending, 1, 2, false)),
		string(tasksKey("a", TaskPending, 1, 20, true)),
		string(groupTasksKey("ab", "c", 1, 20)), string(groupTasksKey("a", "bc", 1, 20)),
		string(taskKey("ab", "c")), string(taskKey("a", "bc")),
		string(queueKey("a")), string(groupsKey("a")),
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Errorf("duplicate key %q", k)
		}
		seen[k] = true
	}
	if !slices.Contains(keys, "queue/a") {
		t.Errorf("queueKey = %q", queueKey("a"))
	}
}

func TestGroupTasks(t *testing.T) {
	agg := func(id, group string) *asynq.TaskInfo {
		info := ti("emails", id, asynq.TaskStateAggregating)
		info.Group = group
		return info
	}
	f := newFakeTasks(agg("g1", "a"), agg("g2", "b"), agg("g3", "a"), agg("g4", "a"))
	c := taskConn(t, f, "emails")

	got, err := c.ListGroupTasks(t.Context(), "emails", "a", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "g1" || got[0].State != TaskAggregating {
		t.Errorf("group a = %+v", got)
	}
	if _, err := c.ListGroupTasks(t.Context(), "nope", "a", 1, 20); !errors.Is(err, ErrQueueNotFound) {
		t.Errorf("unknown queue: err = %v", err)
	}

	n, err := c.ArchiveAllGroupTasks(t.Context(), "emails", "b")
	if err != nil || n != 1 {
		t.Errorf("ArchiveAllGroupTasks = %d, %v; want 1", n, err)
	}
	if got, err := c.ListGroupTasks(t.Context(), "emails", "a", 1, 20); err != nil || len(got) != 3 {
		t.Errorf("after archiving b, group a = %+v, %v", got, err)
	}
	n, err = c.RunAllGroupTasks(t.Context(), "emails", "a")
	if err != nil || n != 3 {
		t.Errorf("RunAllGroupTasks = %d, %v; want 3", n, err)
	}
	if got, err := c.ListGroupTasks(t.Context(), "emails", "a", 1, 20); err != nil || len(got) != 0 {
		t.Errorf("after run all, group a = %+v, %v; want empty (cache dropped)", got, err)
	}
	if n, err := c.DeleteAllGroupTasks(t.Context(), "emails", "a"); err != nil || n != 0 {
		t.Errorf("DeleteAllGroupTasks = %d, %v; want 0", n, err)
	}
	if _, err := c.DeleteAllGroupTasks(t.Context(), "nope", "a"); !errors.Is(err, ErrQueueNotFound) {
		t.Errorf("unknown queue: err = %v", err)
	}
	if f.count("DeleteAllAggregatingTasks") != 1 {
		t.Error("an all action ran on an unknown queue")
	}
}
