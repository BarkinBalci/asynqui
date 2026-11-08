// Package conn owns the connections to Redis: one asynq Inspector, asynq
// Client and Redis client per configured connection, with health checks and a
// short-lived per-connection cache coalesced through singleflight so that many
// viewers cost one Redis round per refresh interval.
package conn
