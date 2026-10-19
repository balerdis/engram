package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gentleman-Programming/engram/internal/portowner"
)

func guarded(t *testing.T, self int, uid int, err error) http.Handler {
	t.Helper()
	srv := New(newServerTestStore(t), 0)
	srv.selfUID = func() int { return self }
	srv.uidOf = func(*http.Request) (int, error) { return uid, err }
	return srv.Handler()
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOwnerGuardForeignIsRefusedExceptHealth(t *testing.T) {
	t.Setenv(envAllowOtherUsers, "")
	h := guarded(t, 1000, 1001, nil)
	for _, c := range [][3]string{{"POST", "/prompts", `{"session_id":"s","content":"x"}`}, {"GET", "/context", ""}, {"GET", "/stats", ""}} {
		rec := do(h, c[0], c[1], c[2])
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: %d", c[0], c[1], rec.Code)
		}
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["code"] != "foreign_uid" || out["error"] == "" {
			t.Fatalf("body %s", rec.Body)
		}
	}
	if rec := do(h, "GET", "/health", ""); rec.Code != http.StatusOK {
		t.Fatalf("health: %d", rec.Code)
	}
}

func TestOwnerGuardPasses(t *testing.T) {
	t.Setenv(envAllowOtherUsers, "")
	cases := map[string]struct {
		self, uid int
		err       error
	}{
		"own":              {1000, 1000, nil},
		"root":             {1000, 0, nil},
		"proc unavailable": {1000, -1, portowner.ErrUnavailable},
		"unknown uid":      {1000, -1, nil},
		"no self uid":      {-1, 1001, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := do(guarded(t, c.self, c.uid, c.err), "GET", "/context", ""); rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
	t.Run("no established row fails closed", func(t *testing.T) {
		for _, err := range []error{portowner.ErrNoRow, errors.New("boom")} {
			if rec := do(guarded(t, 1000, -1, err), "GET", "/context", ""); rec.Code != http.StatusForbidden {
				t.Fatalf("%v: %d", err, rec.Code)
			}
		}
	})
	t.Run("switch", func(t *testing.T) {
		t.Setenv(envAllowOtherUsers, "1")
		if rec := do(guarded(t, 1000, 1001, nil), "GET", "/context", ""); rec.Code != http.StatusOK {
			t.Fatalf("%d", rec.Code)
		}
	})
	t.Run("no connection recorded fails open", func(t *testing.T) {
		srv := New(newServerTestStore(t), 0)
		srv.selfUID = func() int { return 1000 }
		if rec := do(srv.Handler(), "GET", "/context", ""); rec.Code != http.StatusOK {
			t.Fatalf("%d", rec.Code)
		}
	})
}

func TestHealthReportsUIDAndVersion(t *testing.T) {
	srv := New(newServerTestStore(t), 0)
	srv.SetVersion("1.22.0")
	srv.selfUID = func() int { return 4321 }
	var out map[string]any
	if err := json.Unmarshal(do(srv.Handler(), "GET", "/health", "").Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["version"] != "1.22.0" || out["uid"] != float64(4321) || out["status"] != "ok" {
		t.Fatalf("%v", out)
	}
	if New(newServerTestStore(t), 0).version == "0.1.0" {
		t.Fatal("hard-coded version")
	}
}

// e2eServer starts a real listener whose ConnContext reads fake /proc tables.
func e2eServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	t.Setenv(envAllowOtherUsers, "")
	srv := New(newServerTestStore(t), 0)
	srv.selfUID = func() int { return 1000 }
	hs := httptest.NewUnstartedServer(srv.Handler())
	hs.Config.ConnContext = srv.connContext
	hs.Start()
	t.Cleanup(hs.Close)
	dir := t.TempDir()
	tcp4 := filepath.Join(dir, "tcp")
	t.Setenv(portowner.EnvTCP4, tcp4)
	t.Setenv(portowner.EnvTCP6, filepath.Join(dir, "tcp6"))
	return srv, hs, tcp4
}

// freePort reserves a loopback port the client will dial from, so its row can be
// written to the fake table before it connects.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func writeRow(t *testing.T, tcp4 string, clientPort int, server netip.AddrPort, state string, uid int) {
	t.Helper()
	c := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(clientPort))
	body := "  sl\n" + fmt.Sprintf("   0: %s %s %s 0:0 0:0 0 %d 0 1\n", ep(c), ep(server), state, uid)
	if err := os.WriteFile(tcp4, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixedPortClient(port int) *http.Client {
	d := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}}
	return &http.Client{Transport: &http.Transport{DialContext: d.DialContext}}
}

func status(t *testing.T, c *http.Client, url string) int {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestConnContextEndToEnd(t *testing.T) {
	_, hs, tcp4 := e2eServer(t)
	server := netip.MustParseAddrPort(strings.TrimPrefix(hs.URL, "http://"))

	cases := []struct {
		name  string
		state string
		uid   int
		want  int
	}{
		{"own established", "01", 1000, 200},
		{"root established", "01", 0, 200},
		{"foreign established", "01", 1001, 403},
		{"closed socket reported as root", "05", 0, 403},
		{"time wait reported as root", "06", 0, 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			port := freePort(t)
			writeRow(t, tcp4, port, server, c.state, c.uid)
			cl := fixedPortClient(port)
			defer cl.CloseIdleConnections()
			if got := status(t, cl, hs.URL+"/context"); got != c.want {
				t.Fatalf("got %d want %d", got, c.want)
			}
			// Keep-alive: same connection, cached answer even when the row vanishes.
			os.Remove(tcp4)
			os.WriteFile(tcp4, []byte("  sl\n"), 0o600)
			if got := status(t, cl, hs.URL+"/context"); got != c.want {
				t.Fatalf("keep-alive got %d want %d", got, c.want)
			}
			if got := status(t, cl, hs.URL+"/health"); got != 200 {
				t.Fatalf("health %d", got)
			}
		})
	}

	t.Run("readable table without a row is refused", func(t *testing.T) {
		os.WriteFile(tcp4, []byte("  sl\n"), 0o600)
		cl := &http.Client{Transport: &http.Transport{}}
		defer cl.CloseIdleConnections()
		if got := status(t, cl, hs.URL+"/context"); got != http.StatusForbidden {
			t.Fatalf("got %d", got)
		}
	})
	t.Run("missing /proc fails open", func(t *testing.T) {
		os.Remove(tcp4)
		cl := &http.Client{Transport: &http.Transport{}}
		defer cl.CloseIdleConnections()
		if got := status(t, cl, hs.URL+"/context"); got != http.StatusOK {
			t.Fatalf("got %d", got)
		}
	})
}

// A raw client that sends a whole write and closes at once must not get it applied:
// the owner is resolved at accept time and a closed socket row is never trusted.
func TestSendAndCloseClientIsNotTrusted(t *testing.T) {
	srv, hs, tcp4 := e2eServer(t)
	server := netip.MustParseAddrPort(strings.TrimPrefix(hs.URL, "http://"))
	for _, mode := range []string{"close", "rst"} {
		t.Run(mode, func(t *testing.T) {
			// The kernel would show the closed socket as uid 0 in FIN_WAIT2.
			port := freePort(t)
			writeRow(t, tcp4, port, server, "05", 0)
			d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}}
			conn, err := d.Dial("tcp", server.String())
			if err != nil {
				t.Fatal(err)
			}
			body := `{"id":"sess-` + mode + `","project":"p","directory":"/x"}`
			fmt.Fprintf(conn, "POST /sessions HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
			if mode == "rst" {
				conn.(*net.TCPConn).SetLinger(0)
			}
			conn.Close()
			time.Sleep(300 * time.Millisecond)
			if sess, _ := srv.store.GetSession("sess-" + mode); sess != nil {
				t.Fatalf("write from an untrusted closed client was applied")
			}
		})
	}
}

func ep(a netip.AddrPort) string {
	b := a.Addr().As4()
	return fmt.Sprintf("%02X%02X%02X%02X:%04X", b[3], b[2], b[1], b[0], a.Port())
}
