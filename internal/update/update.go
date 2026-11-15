package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

const (
	// DefaultURL is the GitHub API endpoint for the latest release.
	DefaultURL = "https://api.github.com/repos/BarkinBalci/asynqui/releases/latest"
	// interval is the time between two checks.
	interval = 24 * time.Hour
	// defaultTimeout bounds one check.
	defaultTimeout = 5 * time.Second
	// maxBody caps how much of the answer is read.
	maxBody = 1 << 20
)

// ErrNotRelease is returned by New for a running version that is not a
// release: not a semantic version (such as "dev"), or one with a prerelease
// part. Such builds are never checked.
var ErrNotRelease = errors.New("not a release version")

// Options configure a Checker. Version is required.
type Options struct {
	// Version is the running version, e.g. "0.1.0" or "v0.1.0".
	Version string
	// Logger receives the check's debug and info messages. Nil discards
	// them.
	Logger *slog.Logger
	// Client sends the request. Nil means a new http.Client.
	Client *http.Client
	// After is the clock that spaces the checks. Nil means time.After.
	After func(time.Duration) <-chan time.Time
	// URL is the releases API endpoint. Empty means DefaultURL.
	URL string
	// Timeout bounds one check. Zero means defaultTimeout.
	Timeout time.Duration
}

// Checker periodically looks up the latest GitHub release and remembers it
// when it is newer than the running version. It is safe for concurrent use.
type Checker struct {
	version string
	current string // canonical, "v1.2.3"
	logger  *slog.Logger
	client  *http.Client
	after   func(time.Duration) <-chan time.Time
	url     string
	timeout time.Duration

	mu     sync.RWMutex
	latest string // tag of the newer release; "" when none is known
}

// New returns a Checker for o.Version, or ErrNotRelease when that is not a
// release version.
func New(o Options) (*Checker, error) {
	cur, ok := releaseVersion(o.Version)
	if !ok {
		return nil, fmt.Errorf("version %q: %w", o.Version, ErrNotRelease)
	}
	c := &Checker{
		version: o.Version,
		current: cur,
		logger:  o.Logger,
		client:  o.Client,
		after:   o.After,
		url:     o.URL,
		timeout: o.Timeout,
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	if c.client == nil {
		c.client = &http.Client{}
	}
	if c.after == nil {
		c.after = time.After
	}
	if c.url == "" {
		c.url = DefaultURL
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	return c, nil
}

// Latest returns the newest release's tag, such as "v0.2.0", with ok true,
// when a release newer than the running version is known. The tag is a
// semantic version, so it is safe in a URL path segment once escaped.
func (c *Checker) Latest() (version string, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest, c.latest != ""
}

// Run checks now and then every interval until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	for {
		c.check(ctx)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-c.after(interval):
		}
	}
}

// release is the part of GitHub's release object the check reads.
type release struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// check runs one lookup and updates the result. A failure only logs, at
// debug level, and keeps the last result.
func (c *Checker) check(ctx context.Context) {
	rel, err := c.fetch(ctx)
	if err != nil {
		c.logger.Debug("update check failed", slog.Any("error", err))
		return
	}
	v, ok := releaseVersion(rel.TagName)
	if !ok || rel.Draft || rel.Prerelease {
		c.logger.Debug("update check ignored a release", slog.String("tag", rel.TagName),
			slog.Bool("draft", rel.Draft), slog.Bool("prerelease", rel.Prerelease))
		return
	}
	newer := semver.Compare(c.current, v) < 0

	c.mu.Lock()
	changed := newer && c.latest != rel.TagName
	if newer {
		c.latest = rel.TagName
	} else {
		c.latest = ""
	}
	c.mu.Unlock()

	if changed {
		c.logger.Info("update available", slog.String("version", c.version), slog.String("latest", rel.TagName))
	}
}

func (c *Checker) fetch(ctx context.Context) (release, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return release{}, err
	}
	req.Header.Set("User-Agent", "asynqui/"+c.version)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.client.Do(req)
	if err != nil {
		return release{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("unexpected status %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("decode release: %w", err)
	}
	return rel, nil
}
