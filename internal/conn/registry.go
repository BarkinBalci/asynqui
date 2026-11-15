package conn

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"

	"github.com/BarkinBalci/asynqui/internal/config"
)

// Errors.
var (
	// ErrNotFound is returned for a connection name that is not configured.
	ErrNotFound = errors.New("conn: unknown connection")
	// ErrClosed is returned by health checks after Registry.Close.
	ErrClosed = errors.New("conn: registry closed")
)

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

// Start runs one health loop per connection in the background, each checking
// immediately and then every healthInterval, until ctx is canceled or Close is
// called. It does not block. Calls after the first, or after Close, do
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

// Recheck runs a health check of the named connection now; see
// Conn.Recheck. It returns ErrNotFound for an unknown name.
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
	for _, c := range r.conns {
		c.closed.Store(true) // checks from now on do nothing
	}
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
	db   int
	// tls is the TLS setting; username is the ACL user, empty for Redis's
	// default user.
	tls      TLSInfo
	username string
	// tlsVersion is the version of the last TLS handshake, written by
	// the TLS config's VerifyConnection.
	tlsVersion *atomic.Uint32
	// timeout is the context deadline of our own commands (PING, INFO):
	// dial_timeout + write_timeout + read_timeout, room for one attempt on
	// a fresh connection. Inspector calls take no context; see newConn.
	timeout time.Duration
	logger  *slog.Logger

	inspector *asynq.Inspector
	redis     redis.UniversalClient

	pinger     pinger
	info       infoReader
	queues     queueInspector
	tasks      taskInspector
	servers    serverInspector
	schedulers schedulerInspector
	keys       keyReader
	cache      *cache
	now        func() time.Time

	closed atomic.Bool // set by Registry.Close
	// backend is backendRedis or backendValkey; 0 before any detection.
	backend atomic.Int32

	mu            sync.Mutex
	health        Health
	healthStarted time.Time // when the check behind health started
}

// maxRetries is how often go-redis retries a failed command (on a network
// error or timeout). One retry still covers a pooled connection that died with
// a Redis restart.
const maxRetries = 1

// newConn builds a connection without any Redis I/O. The Inspector and our own
// commands share one go-redis client, and so one connection pool, with the
// options asynq.RedisClientOpt.MakeRedisClient sets plus
// ContextTimeoutEnabled, so that context deadlines bound our own commands, and
// maxRetries. Inspector methods take no context, so only the socket timeouts
// bound them, and nothing cancels them early: a viewer that leaves only stops
// waiting (see cached).
func newConn(cc config.Connection, logger *slog.Logger) (*Conn, error) {
	tc, err := tlsConfig(cc.TLS)
	if err != nil {
		return nil, err
	}
	tlsVersion := new(atomic.Uint32)
	if tc != nil {
		// Runs after the standard verification of every handshake (and
		// with insecure_skip_verify too); it only records the version.
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			tlsVersion.Store(uint32(cs.Version))
			return nil
		}
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
		MaxRetries:            maxRetries,
	})
	inspector := asynq.NewInspectorFromRedisClient(rc)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Conn{
		name:       cc.Name,
		addr:       displayAddr(cc.Addr, cc.DB),
		db:         cc.DB,
		tls:        TLSInfo{Enabled: cc.TLS.Enabled, Verified: cc.TLS.Enabled && !cc.TLS.InsecureSkipVerify},
		tlsVersion: tlsVersion,
		username:   cc.Username,
		timeout:    cc.DialTimeout + cc.WriteTimeout + cc.ReadTimeout,
		logger:     logger.With(slog.String("conn", cc.Name)),
		inspector:  inspector,
		redis:      rc,
		pinger:     redisPinger{rc},
		info:       redisInfo{rc},
		queues:     inspector,
		tasks:      inspector,
		servers:    inspector,
		schedulers: inspector,
		keys:       redisKeys{rc},
		cache:      newCache(),
		now:        time.Now,
	}, nil
}

// TLSInfo is a connection's TLS setting, from config, and the version of
// its last TLS handshake.
type TLSInfo struct {
	Enabled bool
	// Verified is false when tls.insecure_skip_verify is set.
	Verified bool
	// Version is the tls.VersionTLS1x of the last handshake; 0 before any
	// (or without TLS).
	Version uint16
}

// TLS returns the connection's TLS setting and last negotiated version.
func (c *Conn) TLS() TLSInfo {
	t := c.tls
	if c.tlsVersion != nil {
		t.Version = uint16(c.tlsVersion.Load()) //nolint:gosec // G115: stored from a uint16
	}
	return t
}

// Username returns the configured ACL username; empty is Redis's default
// user.
func (c *Conn) Username() string { return c.username }

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
