package main

import (
	"io"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
)

// stdio adapts this process's stdin/stdout into the single ReadWriteCloser
// net/rpc wants. Stash's plugin runner (pkg/plugin/rpc.go) connects a
// JSON-RPC *client* codec to the child's pipes via natefinch/pie's
// StartProviderCodec; the provider side is nothing more than a JSON-RPC
// *server* codec over the same two streams — no handshake, no framing beyond
// what the codec itself does.
type stdio struct {
	io.Reader
	io.Writer
}

func (stdio) Close() error { return nil }

// servePlugin registers r under the service name Stash calls ("RPCRunner")
// and serves until stdin closes — which is how Stash tells a provider the
// session is over.
func servePlugin(r *runner) error {
	s := rpc.NewServer()
	if err := s.RegisterName("RPCRunner", r); err != nil {
		return err
	}
	s.ServeCodec(jsonrpc.NewServerCodec(stdio{os.Stdin, os.Stdout}))
	return nil
}
