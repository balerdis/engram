package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		"own":            {1000, 1000, nil},
		"root":           {1000, 0, nil},
		"resolver error": {1000, 1001, errors.New("no /proc")},
		"unknown uid":    {1000, -1, nil},
		"no self uid":    {-1, 1001, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := do(guarded(t, c.self, c.uid, c.err), "GET", "/context", ""); rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
		})
	}
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

// A real connection through the default serve path: the client's socket row is
// found in a fake table (injected through the env seams, never the real /proc)
// and keep-alive requests reuse the cached answer.
func TestConnContextEndToEnd(t *testing.T) {
	t.Setenv(envAllowOtherUsers, "")
	srv := New(newServerTestStore(t), 0)
	srv.selfUID = func() int { return 1000 }
	hs := httptest.NewUnstartedServer(srv.Handler())
	hs.Config.ConnContext = srv.connContext
	hs.Start()
	defer hs.Close()

	server := netip.MustParseAddrPort(strings.TrimPrefix(hs.URL, "http://"))
	dir := t.TempDir()
	tcp4 := filepath.Join(dir, "tcp")
	t.Setenv(portowner.EnvTCP4, tcp4)
	t.Setenv(portowner.EnvTCP6, filepath.Join(dir, "tcp6"))

	// The client port is only known once connected, so the resolver writes its row.
	foreign := true
	client := &http.Client{Transport: &http.Transport{}}
	get := func(path string) int {
		resp, err := client.Get(hs.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	// Learn the client port with a /health call, then publish its row.
	conn := make(chan netip.AddrPort, 1)
	srv.uidOf = func(r *http.Request) (int, error) {
		c, ok := r.Context().Value(ctxKey{}).(*connOwner)
		if !ok {
			return -1, errNoConn
		}
		select {
		case conn <- c.client:
		default:
		}
		uid := 1000
		if foreign {
			uid = 1001
		}
		body := "  sl\n" + fmt.Sprintf("   0: %s %s 01 0:0 0:0 0 %d 0 1\n", ep(c.client), ep(server), uid)
		if err := os.WriteFile(tcp4, []byte(body), 0o600); err != nil {
			return -1, err
		}
		return c.resolve()
	}
	if code := get("/context"); code != http.StatusForbidden {
		t.Fatalf("foreign client: %d", code)
	}
	// Same keep-alive connection: the cached uid is reused even if the row vanishes.
	_ = os.Remove(tcp4)
	if code := get("/context"); code != http.StatusForbidden {
		t.Fatalf("cached uid not reused: %d", code)
	}
	if code := get("/health"); code != http.StatusOK {
		t.Fatalf("health: %d", code)
	}
	if got := <-conn; !got.Addr().IsLoopback() {
		t.Fatalf("client %v", got)
	}
}

func ep(a netip.AddrPort) string {
	b := a.Addr().As4()
	return fmt.Sprintf("%02X%02X%02X%02X:%04X", b[3], b[2], b[1], b[0], a.Port())
}
