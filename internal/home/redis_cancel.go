package home

import (
	"context"
	"net"
)

// cancelableRedisDialer binds dedicated RPC sockets to their owning context.
// Use the underlying transport close so TLS shutdown cannot block cancellation.
func cancelableRedisDialer(ctx context.Context, dialer func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(dialCtx context.Context, network, address string) (net.Conn, error) {
		conn, errDial := dialer(dialCtx, network, address)
		if errDial != nil {
			return nil, errDial
		}
		if ctx.Err() != nil {
			_ = closeUnderlyingTransport(conn)
			return nil, context.Cause(ctx)
		}
		return newPluginSyncCancelableConn(ctx, conn), nil
	}
}
