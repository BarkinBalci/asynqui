// Package conn owns the connections to Redis: one asynq Inspector and one
// Redis client per configured connection, with health checks and a
// short-lived per-connection cache that coalesces concurrent fetches, so that
// many viewers cost one Redis round per refresh interval.
//
// NewRegistry builds every connection without Redis I/O. Start runs a
// background health loop per connection (PING every 10s); Health reads its
// last result and Recheck runs one now. Summary and Info fetch through the
// cache: a value is served for 1s, concurrent fetches of the same key are
// shared, and errors are not cached.
package conn
