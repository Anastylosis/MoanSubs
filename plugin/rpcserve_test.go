package main

import (
	"io"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"testing"
)

// TestRPCRoundTrip drives the runner exactly the way Stash's plugin host
// does: a JSON-RPC client codec calling RPCRunner.Run over a byte stream.
// If the service name, method signature, or codec ever drift from the
// protocol, this fails here instead of silently inside a live Stash.
func TestRPCRoundTrip(t *testing.T) {
	hostSide, pluginSide := net.Pipe()

	s := rpc.NewServer()
	if err := s.RegisterName("RPCRunner", &runner{}); err != nil {
		t.Fatal(err)
	}
	go s.ServeCodec(jsonrpc.NewServerCodec(pluginSide))

	client := rpc.NewClientWithCodec(jsonrpc.NewClientCodec(hostSide))
	defer client.Close()

	// An unknown mode must come back as PluginOutput.Error — an RPC-level
	// error would crash the task with no useful message in Stash's log.
	var out PluginOutput
	err := client.Call("RPCRunner.Run", PluginInput{
		ServerConnection: ServerConnection{Scheme: "http", Host: "localhost", Port: 9999},
		Args:             map[string]any{"mode": "definitely-not-a-mode"},
	}, &out)
	if err != nil {
		t.Fatalf("RPC transport error: %v", err)
	}
	if out.Error == nil {
		t.Fatal("want PluginOutput.Error for unknown mode, got nil")
	}

	var stopped bool
	if err := client.Call("RPCRunner.Stop", struct{}{}, &stopped); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !stopped {
		t.Error("Stop returned false")
	}
}

// stdio must satisfy io.ReadWriteCloser for the codec.
var _ io.ReadWriteCloser = stdio{}
