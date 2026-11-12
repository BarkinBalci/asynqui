package conn

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/redis/go-redis/v9"
)

// Unreachable reports whether err means the server could not be reached
// or did not answer in time: a network error (refused, reset, closed), a
// timeout, or no pool connection in time. An error reply from the server
// (NOPERM, NOAUTH, LOADING, …) is not: the server answered, so the
// connection is up and only that read failed.
func Unreachable(err error) bool {
	if err == nil {
		return false
	}
	var reply redis.Error
	if errors.As(err, &reply) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, redis.ErrPoolTimeout) || errors.Is(err, redis.ErrClosed)
}
