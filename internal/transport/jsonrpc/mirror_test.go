package jsonrpc

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestMirrorMatchesEpisodeStderr(t *testing.T) {
	p := fake()
	var mirror bytes.Buffer
	c := client(t, p, Options{Mirror: &mirror})
	raw := []byte("dataset text token=fixture-secret\n" + strings.Repeat("tail\n", 20000))
	go func() {
		for off := 0; off < len(raw); off += 97 {
			p.errW.Write(raw[off:min(len(raw), off+97)])
		}
		p.finish(0)
	}()
	<-c.stderrDone
	c.Close()
	got, e := os.ReadFile(c.log.Name())
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, raw) || !bytes.Equal(mirror.Bytes(), raw) {
		t.Fatal("mirror changed/dropped original stderr bytes")
	}
	if !strings.Contains(c.DiagnosticLog(), "[REDACTED]") || strings.Contains(c.DiagnosticLog(), "token=fixture-secret") {
		t.Fatal("installer diagnostic redaction changed")
	}
	if len(c.tail) != 64<<10 {
		t.Fatal("existing tail capacity changed")
	}
}
