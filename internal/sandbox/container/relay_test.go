package container

import (
	"io"
	"net"
	"strings"
	"testing"
)

func TestRelayRejectsDNSAndReady(t *testing.T) {
	for _, target := range []string{"localhost:80", "example.invalid:8317", "127.0.0.1:0", "127.0.0.1:65536", "http://127.0.0.1:80", "[fe80::1%en0]:80"} {
		if e := literalEndpoint(target, false); e == nil || !strings.Contains(e.Error(), "no DNS") {
			t.Fatal(target, e)
		}
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := ln.Addr().String()
	if e := ReadyCommand([]string{"--tcp", addr, "--timeout", "1s"}, io.Discard); e != nil {
		t.Fatal(e)
	}
	ln.Close()
	if e := ReadyCommand([]string{"--tcp", addr, "--timeout", "20ms"}, io.Discard); e == nil || !strings.Contains(e.Error(), "readiness failed") {
		t.Fatal(e)
	}
	if e := ReadyCommand([]string{"--tcp", "localhost:8317"}, io.Discard); e == nil {
		t.Fatal("ready resolved DNS")
	}
}
