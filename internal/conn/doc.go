// Package conn owns the connections to Redis: one asynq Inspector and one
// Redis client per configured connection, with health checks and a short-lived
// per-connection cache that coalesces concurrent fetches. Everything goes
// through asynq's public Inspector except keys.go, the one place that reads
// asynq's own keys.
package conn
