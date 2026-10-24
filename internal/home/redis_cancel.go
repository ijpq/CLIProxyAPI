package home

import (
	"context"
	"net"
)

type cancelableRedisConn struct {
	net.Conn
	stopCancel func() bool
}

func (c *cancelableRedisConn) Close() error {
	c.stopCancel()
	return c.Conn.Close()
}

// cancelableRedisDialer binds dedicated RPC transports to their owning context.
// Close the socket directly: go-redis pool shutdown can wait for an active read
// while checking connection health, so closing only the pool cannot cancel it.
func cancelableRedisDialer(ctx context.Context, dialer func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(dialCtx context.Context, network, address string) (net.Conn, error) {
		conn, errDial := dialer(dialCtx, network, address)
		if errDial != nil {
			return nil, errDial
		}
		return &cancelableRedisConn{
			Conn:       conn,
			stopCancel: context.AfterFunc(ctx, func() { _ = conn.Close() }),
		}, nil
	}
}
