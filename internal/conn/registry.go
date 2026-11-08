package conn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// ErrNotFound is returned for a connection name that is not configured.
var ErrNotFound = errors.New("conn: unknown connection")

// Registry holds every configured connection, in config order. It is safe
// for concurrent use.
type Registry struct {
	conns  []*Conn
	byName map[string]*Conn
	logger *slog.Logger

	mu     sync.Mutex
	stop   context.CancelFunc // set by Start
	closed bool
	wg     sync.WaitGroup // health loops
}

// NewRegistry builds a connection per entry of conns, in order. It does no
// Redis I/O, so it never fails because Redis is down; it fails only when a
// connection's TLS files cannot be read. A nil logger discards logs.
func NewRegistry(conns []config.Connection, logger *slog.Logger) (*Registry, error) {
	built := make([]*Conn, 0, len(conns))
	var errs []error
	for _, cc := range conns {
		c, err := newConn(cc, logger)
		if err != nil {
			errs = append(errs, fmt.Errorf("connection %s: %w", cc.Name, err))
			continue
		}
		built = append(built, c)
	}
	if err := errors.Join(errs...); err != nil {
		for _, c := range built {
			_ = c.redis.Close()
		}
		return nil, err
	}
	return newRegistry(logger, built...), nil
}

// newRegistry indexes already built connections. Tests use it with fakes.
func newRegistry(logger *slog.Logger, conns ...*Conn) *Registry {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	r := &Registry{conns: conns, byName: make(map[string]*Conn, len(conns)), logger: logger}
	for _, c := range conns {
		r.byName[c.name] = c
	}
	return r
}

// Get returns the connection with the given name.
func (r *Registry) Get(name string) (*Conn, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// All returns every connection in config order. The slice is a copy.
func (r *Registry) All() []*Conn {
	return append([]*Conn(nil), r.conns...)
}

// Start runs one health loop per connection in the background, each
// checking immediately and then every 10s, until ctx is canceled or Close
// is called. It does not block. Calls after the first, or after Close, do
// nothing.
func (r *Registry) Start(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop != nil || r.closed {
		return
	}
	ctx, r.stop = context.WithCancel(ctx)
	for _, c := range r.conns {
		r.wg.Go(func() { c.healthLoop(ctx, healthInterval) })
	}
}

// Recheck runs a health check of the named connection now and stores its
// result, which it returns. It returns ErrNotFound for an unknown name, and
// ctx's error, without storing anything, if ctx ends first.
func (r *Registry) Recheck(ctx context.Context, name string) (Health, error) {
	c, ok := r.Get(name)
	if !ok {
		return Health{}, ErrNotFound
	}
	return c.Recheck(ctx)
}

// Close stops the health loops, waits for them to return, then closes every
// connection's Redis client. It returns the close errors joined. Calls after
// the first return nil.
func (r *Registry) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	if r.stop != nil {
		r.stop()
	}
	r.mu.Unlock()
	r.wg.Wait()

	var errs []error
	for _, c := range r.conns {
		if err := c.redis.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close connection %s: %w", c.name, err))
		}
	}
	return errors.Join(errs...)
}

// Conn is one configured connection: a Redis address plus logical database.
// It is safe for concurrent use.
type Conn struct {
	name string
	addr string // display address, host:port/db
	// timeout bounds one Redis operation that may need a fresh connection:
	// dial (with TLS and AUTH), write and read.
	timeout time.Duration
	logger  *slog.Logger

	inspector *asynq.Inspector
	client    *asynq.Client
	redis     redis.UniversalClient

	pinger pinger         // redis, or a fake in tests
	queues queueInspector // inspector, or a fake in tests
	cache  *cache

	mu     sync.Mutex
	health Health
}

// newConn builds a connection without any Redis I/O.
//
// The Inspector, the Client and our own commands share one go-redis client,
// and so one connection pool, instead of the three pools that
// asynq.NewInspector and asynq.NewClient with a RedisClientOpt would open.
// Its options are those asynq.RedisClientOpt.MakeRedisClient sets, plus
// ContextTimeoutEnabled so that context deadlines bound our own commands
// (PING, INFO) and Client.EnqueueContext. Inspector methods take no context
// and run under context.Background() inside asynq, so only the socket
// timeouts bound them.
func newConn(cc config.Connection, logger *slog.Logger) (*Conn, error) {
	tc, err := tlsConfig(cc.TLS)
	if err != nil {
		return nil, err
	}
	rc := redis.NewClient(&redis.Options{
		Addr:                  cc.Addr,
		Username:              cc.Username,
		Password:              cc.Password.Reveal(),
		DB:                    cc.DB,
		DialTimeout:           cc.DialTimeout,
		ReadTimeout:           cc.ReadTimeout,
		WriteTimeout:          cc.WriteTimeout,
		TLSConfig:             tc,
		ContextTimeoutEnabled: true,
	})
	inspector := asynq.NewInspectorFromRedisClient(rc)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Conn{
		name:      cc.Name,
		addr:      displayAddr(cc.Addr, cc.DB),
		timeout:   cc.DialTimeout + cc.WriteTimeout + cc.ReadTimeout,
		logger:    logger.With(slog.String("conn", cc.Name)),
		inspector: inspector,
		client:    asynq.NewClientFromRedisClient(rc),
		redis:     rc,
		pinger:    redisPinger{rc},
		queues:    inspector,
		cache:     newCache(),
	}, nil
}

// displayAddr formats the address shown under a connection's name.
func displayAddr(addr string, db int) string {
	return addr + "/" + strconv.Itoa(db)
}

// Name returns the connection's unique name.
func (c *Conn) Name() string { return c.name }

// Addr returns the display address, host:port/db.
func (c *Conn) Addr() string { return c.addr }

// Inspector returns the connection's asynq Inspector. It shares the
// Registry's Redis client: do not Close it.
func (c *Conn) Inspector() *asynq.Inspector { return c.inspector }

// Client returns the connection's asynq Client. It shares the Registry's
// Redis client: do not Close it.
func (c *Conn) Client() *asynq.Client { return c.client }

// Redis returns the connection's Redis client. The Registry owns it: do not
// Close it.
func (c *Conn) Redis() redis.UniversalClient { return c.redis }
