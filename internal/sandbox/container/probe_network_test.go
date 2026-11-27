package container

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestGatewayProbeUnauthenticatedAndNoRedirect(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fake-gateway-key")
	var code atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected probe headers/path: %v %s", r.Header, r.URL.Path)
		}
		w.Header().Set("Location", "http://must-not-resolve.invalid/")
		w.WriteHeader(int(code.Load()))
	}))
	defer s.Close()
	for _, status := range []int{200, 401, 403, 302, 404, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			code.Store(int64(status))
			e := requestStatus(s.URL+"/v1/models", nil, 200, 401, 403)
			want := status == 200 || status == 401 || status == 403
			if (e == nil) != want {
				t.Fatal(status, e)
			}
		})
	}
}

func TestReadyHTTP(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer s.Close()
	if e := ReadyCommand([]string{"--http", s.URL, "--expect", "204", "--timeout", "1s"}, io.Discard); e != nil {
		t.Fatal(e)
	}
	if e := ReadyCommand([]string{"--http", s.URL, "--status", "200", "--timeout", "50ms"}, io.Discard); e == nil {
		t.Fatal("wrong HTTP status accepted")
	}
}
