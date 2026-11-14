package pages

import (
	"strings"
	"time"

	"github.com/BarkinBalci/asynqui/internal/web/views"
	"github.com/BarkinBalci/asynqui/internal/web/views/components"
)

// Live is the polling frame of the connection pages: Connection, Queue,
// Task, Servers, Server detail, Schedulers, Scheduler entry and Redis info.
//
// Requests: the element with id PageID wraps the page and is the hx-sync
// scope of every request it makes. The hidden poll element (PollID) polls
// PollURL with hx-swap none; it is dropped while another request is in
// flight and aborted by a user request (a sort or page link), whose answer
// replaces the regions it changes and pushes the URL. Answers swap regions
// out of band, by id: the poll element itself, the state banner
// (BannerID, LiveStateID when empty), the page's data regions, the Updated
// time and the sidebar list. An empty PollURL stops the polling.
type Live struct {
	PageID   string
	PollID   string
	BannerID string
	PollURL  string
	// State and Error say whether the page has its data.
	State DataState
	Error string
	// Down marks a failure to reach the server ("Connection failed"); any
	// other failure is the read's own error (NOPERM, LOADING).
	Down bool
	// Poll marks the answer to a poll, Init one from a page rendered
	// without its data regions, which gets every region.
	Poll bool
	Init bool
	// Keep marks a poll that found no data while the page shows some: the
	// data regions are left alone and the banner says why.
	Keep bool
}

// LiveStateID is the state banner of a Live page.
const LiveStateID = "live-state"

// Element ids of the Servers, Schedulers and Redis info pages. Focusable
// elements in the polled regions have stable ids too (sort-, slink-,
// wtask-, wqueue-, elink-, etask-, hist- prefixes).
const (
	ServersPageID    = "servers-page"
	ServersPollID    = "servers-poll"
	ServersTableID   = "servers-table"
	ServersRowsID    = "servers-rows"
	ServersFilterID  = "servers-filter"
	ServerPageID     = "server-page"
	ServerPollID     = "server-poll"
	ServerBodyID     = "server-body"
	ServerStateID    = "server-state"
	ServerWorkersID  = "server-workers"
	ServerDetailsID  = "server-details"
	SchedulersPageID = "schedulers-page"
	SchedulersPollID = "schedulers-poll"
	SchedulersTable  = "schedulers-table"
	SchedulersRowsID = "schedulers-rows"
	SchedulersFilter = "schedulers-filter"
	EntryPageID      = "entry-page"
	EntryPollID      = "entry-poll"
	EntryBodyID      = "entry-body"
	EntryHistoryID   = "entry-history"
	EntryDetailsID   = "entry-details"
	InfoPageID       = "info-page"
	InfoPollID       = "info-poll"
	InfoCardsID      = "info-cards"
	InfoRawID        = "info-raw"
	CopyIDButtonID   = "copy-id"
	PayloadID        = "payload"
	RawInfoID        = "raw-info"
)

// ServerColumn is a sortable column of the servers table. Queues does not
// sort.
type ServerColumn int

// Server columns that sort, in display order.
const (
	SrvColHost ServerColumn = iota
	SrvColPID
	SrvColState
	SrvColBusy
	SrvColStarted
	NumServerColumns
)

// ServerStatus is asynq's server status.
type ServerStatus int

// Server statuses. StatusOther shows asynq's text as is.
const (
	StatusActive ServerStatus = iota
	StatusStopped
	StatusOther
)

// QueueToken is one queue of a server, with its priority weight.
type QueueToken struct {
	Name   string
	Weight int64
}

// Servers is the Servers page.
type Servers struct {
	Layout views.Layout
	Live   Live
	Rows   []ServerRow
	Heads  [NumServerColumns]components.SortHead
}

// ServerRow is one server in the table.
type ServerRow struct {
	ID     string
	Host   string
	URL    string
	LinkID string
	PID    int64
	Status ServerStatus
	// StatusText is asynq's status, shown for StatusOther.
	StatusText  string
	Busy        int64
	Concurrency int64
	Queues      []QueueToken
	Started     time.Time
}

// serverFilterText is what the filter matches: host, ID and queues.
func serverFilterText(r ServerRow) string {
	parts := []string{r.Host, r.ID}
	for _, q := range r.Queues {
		parts = append(parts, q.Name)
	}
	return strings.Join(parts, " ")
}

// WorkerColumn is a sortable column of the active workers table.
type WorkerColumn int

// Worker columns that sort.
const (
	WrkColStarted WorkerColumn = iota
	WrkColRunning
	NumWorkerColumns
)

// Server is the Server detail page. The heading (host, ID, Copy ID) is
// rendered once; polls replace only the state badge, the workers and the
// details, unless Whole.
type Server struct {
	Layout views.Layout
	Live   Live
	// Found is false when the server is gone (or never was): the page
	// shows "Server not found".
	Found bool
	// Whole sends the whole body, heading included: to a page rendered
	// without it (no data, or not found), and when the server is gone.
	Whole      bool
	ServersURL string
	Row        ServerRow
	Strict     bool
	Workers    []WorkerRow
	Heads      [NumWorkerColumns]components.SortHead
}

// WorkerRow is one task a server is processing.
type WorkerRow struct {
	TaskID      string
	ShortID     string
	TaskURL     string
	LinkID      string
	Type        string
	Queue       string
	QueueURL    string
	QueueLinkID string
	Started     time.Time
	Running     time.Duration
	// Long highlights a task running for 10 minutes or more.
	Long bool
}

// EntryColumn is a sortable column of the schedulers table. Payload and
// Options do not sort.
type EntryColumn int

// Entry columns that sort, in display order.
const (
	EntColID EntryColumn = iota
	EntColSpec
	EntColType
	EntColNext
	EntColPrev
	NumEntryColumns
)

// Schedulers is the Schedulers page.
type Schedulers struct {
	Layout views.Layout
	Live   Live
	Rows   []EntryRow
	Heads  [NumEntryColumns]components.SortHead
}

// OptionToken is one option of a scheduler entry, shown as name=value.
type OptionToken struct {
	Name  string
	Value string
}

// EntryRow is one scheduler entry.
type EntryRow struct {
	ID       string
	ShortID  string
	URL      string
	LinkID   string
	Spec     string
	TaskType string
	// Payload is a one-line preview; PayloadTitle a longer one.
	Payload      string
	PayloadTitle string
	Options      []OptionToken
	Next         time.Time
	// Prev is zero before the first enqueue.
	Prev time.Time
}

// SchedulerEntry is the Scheduler entry page. The heading (task type, ID,
// Copy ID) and the payload card are rendered once; polls replace only the
// history and the details, unless Whole.
type SchedulerEntry struct {
	Layout views.Layout
	Live   Live
	// Found is false for a stale or unknown entry ID: the page shows
	// "Entry not found".
	Found bool
	// Whole sends the whole body: see Server.Whole.
	Whole         bool
	SchedulersURL string
	ID            string
	TaskType      string
	Spec          string
	// Queue is empty when the entry has no Queue option: asynq does not
	// record a queue set on the task, so it is unknown and shown as "—".
	Queue    string
	QueueURL string
	Options  []OptionToken
	Payload  Payload
	Next     time.Time
	Prev     time.Time
	// Now is the time the relative run times are written against.
	Now time.Time
	// History is the enqueue history page.
	History History
}

// Payload is the payload card of the Task and Scheduler entry pages: the
// payload as shown (indented JSON, or the raw text: Raw, or not JSON;
// empty for no payload) and, for valid JSON, the JSON/Raw toggle, whose
// links (?payload=raw) are JSONURL and RawURL.
type Payload struct {
	Text    string
	JSON    bool
	Raw     bool
	JSONURL string
	RawURL  string
}

// History is one page of an entry's enqueue history, newest first.
type History struct {
	// State is the history read's own: it can miss the page budget or
	// fail while the entry was read.
	State  DataState
	Error  string
	Events []EventRow
	// From and To number the page's events (1-based); both 0 when empty.
	From, To int64
	PrevURL  string
	NextURL  string
}

// EventRow is one enqueue event.
type EventRow struct {
	TaskID  string
	ShortID string
	// TaskURL is empty when the task is gone or the queue unknown: the ID
	// is plain text.
	TaskURL    string
	LinkID     string
	EnqueuedAt time.Time
}

// Info is the Redis / Valkey info page.
type Info struct {
	Layout views.Layout
	Live   Live
	Addr   string
	Valkey bool
	Data   InfoData
}

// InfoData is what the info cards and the raw block show.
type InfoData struct {
	Version          string
	RedisVersion     string
	Mode             string
	Uptime           time.Duration
	ConnectedClients int64
	UsedMemory       int64
	UsedMemoryPeak   int64
	// MaxMemory 0 means no limit.
	MaxMemory       int64
	MaxMemoryPolicy string
	DB              int64
	Keys            int64
	Expires         int64
	TLS             bool
	TLSVerified     bool
	// TLSVersion is the last handshake's tls.VersionTLS1x; 0 unknown.
	TLSVersion uint16
	// Username is the ACL user; empty is the default user.
	Username string
	Raw      string
}
