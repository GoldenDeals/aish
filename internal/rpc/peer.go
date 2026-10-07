package rpc

import (
	"context"
	"net"
)

type peerKey struct{}

// WithPeer gives ctx the pid of the process that made the call: Serve does
// it for each connection the kernel names the client of.
func WithPeer(ctx context.Context, pid int) context.Context {
	return context.WithValue(ctx, peerKey{}, pid)
}

// Peer is the pid of the process that made the call ctx is of, as the
// kernel told it when that process connected; false when it told none.
// The proxy asks which process group it is in: what only the shell's
// foreground job may do is refused to the rest.
func Peer(ctx context.Context) (int, bool) {
	pid, ok := ctx.Value(peerKey{}).(int)
	return pid, ok && pid > 0
}

// peerContext is a context with the client of c, if the kernel tells it.
func peerContext(c net.Conn) context.Context {
	ctx := context.Background()
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return ctx
	}
	var pid int
	var perr error
	if err := raw.Control(func(fd uintptr) { pid, perr = peerPID(int(fd)) }); err != nil || perr != nil {
		return ctx
	}
	return WithPeer(ctx, pid)
}
