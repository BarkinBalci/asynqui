package conn_test

import (
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/BarkinBalci/asynqui/internal/conn"
)

func TestIntegrationTasks(t *testing.T) {
	_, addr := runRedis(t, redisImage)
	cc := connection("main", addr, 0)
	reg := newRegistry(t, cc)
	c := mustGet(t, reg, "main")
	client := newClient(t, cc)

	var ids []string
	for range 3 {
		info, err := client.Enqueue(asynq.NewTask("email", []byte(`{"n":1}`)), asynq.Queue("q"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, info.ID)
	}
	sched, err := client.Enqueue(asynq.NewTask("later", nil), asynq.Queue("q"), asynq.ProcessIn(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	page, err := c.ListTasks(t.Context(), "q", conn.TaskPending, 1, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].ID != ids[0] || page[1].ID != ids[1] || string(page[0].Payload) != `{"n":1}` {
		t.Errorf("pending page 1 = %+v, want the two oldest", page)
	}
	qi, err := c.QueueInfo(t.Context(), "q")
	if err != nil || qi.Pending != 3 || qi.Scheduled != 1 {
		t.Errorf("QueueInfo = %+v, %v", qi, err)
	}

	got, err := c.Task(t.Context(), "q", sched.ID)
	if err != nil || got.State != conn.TaskScheduled || got.NextProcessAt.IsZero() {
		t.Errorf("Task = %+v, %v", got, err)
	}
	if _, err := c.Task(t.Context(), "q", "nope"); !errors.Is(err, conn.ErrTaskNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	if _, err := c.Task(t.Context(), "nope", sched.ID); !errors.Is(err, conn.ErrQueueNotFound) {
		t.Errorf("unknown queue: %v", err)
	}

	if err := c.TaskAction(t.Context(), "q", conn.TaskRun, sched.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Task(t.Context(), "q", sched.ID); err != nil || got.State != conn.TaskPending {
		t.Errorf("after run = %+v, %v; want pending at once", got, err)
	}
	if err := c.TaskAction(t.Context(), "q", conn.TaskRun, sched.ID); err == nil {
		t.Error("running a pending task succeeded")
	}
	if err := c.TaskAction(t.Context(), "q", conn.TaskArchive, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.TaskAction(t.Context(), "q", conn.TaskDelete, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := c.TaskAction(t.Context(), "q", conn.TaskCancel, ids[1]); !errors.Is(err, conn.ErrTaskState) {
		t.Errorf("cancel pending: %v, want ErrTaskState", err)
	}
	n, err := c.AllAction(t.Context(), "q", conn.TaskArchive, conn.TaskPending)
	if err != nil || n != 3 {
		t.Errorf("ArchiveAll = %d, %v; want 3", n, err)
	}
	n, err = c.AllAction(t.Context(), "q", conn.TaskDelete, conn.TaskArchived)
	if err != nil || n != 3 {
		t.Errorf("DeleteAll archived = %d, %v; want 3", n, err)
	}
	if _, err := c.AllAction(t.Context(), "nope", conn.TaskRun, conn.TaskArchived); !errors.Is(err, conn.ErrQueueNotFound) {
		t.Errorf("all on unknown queue: %v", err)
	}
	if qi, err := c.QueueInfo(t.Context(), "q"); err != nil || qi.Size != 0 {
		t.Errorf("after all = %+v, %v; want empty", qi, err)
	}
}

// Aggregating tasks: listed and acted on per group, and they keep their
// queue from being deleted although asynq's own empty check skips them.
func TestIntegrationGroups(t *testing.T) {
	_, addr := runRedis(t, redisImage)
	cc := connection("main", addr, 0)
	reg := newRegistry(t, cc)
	c := mustGet(t, reg, "main")
	client := newClient(t, cc)

	for _, g := range []string{"a", "a", "a", "b"} {
		if _, err := client.Enqueue(asynq.NewTask("batch", nil), asynq.Queue("agg"), asynq.Group(g)); err != nil {
			t.Fatal(err)
		}
	}
	single, err := client.Enqueue(asynq.NewTask("batch", nil), asynq.Queue("agg"), asynq.Group("c"))
	if err != nil {
		t.Fatal(err)
	}
	gs, err := c.Groups(t.Context(), "agg")
	if err != nil || len(gs) != 3 || gs[0] != (conn.Group{Name: "a", Size: 3}) {
		t.Fatalf("Groups = %+v, %v", gs, err)
	}
	page, err := c.ListGroupTasks(t.Context(), "agg", "a", 1, 2)
	if err != nil || len(page) != 2 || page[0].State != conn.TaskAggregating || page[0].Group != "a" {
		t.Errorf("ListGroupTasks = %+v, %v", page, err)
	}

	if err := c.DeleteQueue(t.Context(), "agg"); !errors.Is(err, conn.ErrQueueNotEmpty) {
		t.Errorf("delete a queue of aggregating tasks: %v, want ErrQueueNotEmpty", err)
	}

	if err := c.TaskAction(t.Context(), "agg", conn.TaskArchive, single.ID); err != nil {
		t.Errorf("archive an aggregating task: %v", err)
	}
	n, err := c.GroupAction(t.Context(), "agg", "a", conn.TaskRun)
	if err != nil || n != 3 {
		t.Errorf("GroupAction(Run) = %d, %v; want 3", n, err)
	}
	n, err = c.GroupAction(t.Context(), "agg", "b", conn.TaskArchive)
	if err != nil || n != 1 {
		t.Errorf("GroupAction(Archive) = %d, %v; want 1", n, err)
	}
	qi, err := c.QueueInfo(t.Context(), "agg")
	if err != nil || qi.Aggregating != 0 || qi.Pending != 3 || qi.Archived != 2 {
		t.Errorf("after the group actions = %+v, %v", qi, err)
	}
	if n, err := c.GroupAction(t.Context(), "agg", "a", conn.TaskDelete); err != nil || n != 0 {
		t.Errorf("GroupAction(Delete) of an empty group = %d, %v", n, err)
	}
	if _, err := c.GroupAction(t.Context(), "nope", "a", conn.TaskRun); !errors.Is(err, conn.ErrQueueNotFound) {
		t.Errorf("group action on an unknown queue: %v", err)
	}
}
