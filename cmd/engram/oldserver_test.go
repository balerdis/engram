package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
	"testing"

	engramsrv "github.com/Gentleman-Programming/engram/internal/server"
	"github.com/Gentleman-Programming/engram/internal/store"
)

func TestBusyPortHint(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	probe := func(ver string, ok bool) {
		t.Helper()
		prev := probeHealth
		probeHealth = func(int) (string, bool) { return ver, ok }
		t.Cleanup(func() { probeHealth = prev })
	}

	version = "1.22.0"
	probe("1.21.0", true)
	h := busyPortHint(7437)
	for _, want := range []string{"1.21.0", "1.22.0", "7437", "old `engram serve`", "Stop it"} {
		if !strings.Contains(h, want) {
			t.Fatalf("hint lacks %q: %s", want, h)
		}
	}
	probe("1.22.0", true)
	if h := busyPortHint(7437); !strings.Contains(h, "nothing to start") || strings.Contains(h, "old `engram serve`") {
		t.Fatalf("same version: %s", h)
	}
	probe("", false) // something that is not engram holds the port
	if h := busyPortHint(7437); h != "" {
		t.Fatalf("not an engram server: %s", h)
	}
	version = "dev"
	probe("1.21.0", true)
	if h := busyPortHint(7437); strings.Contains(h, "old `engram serve`") {
		t.Fatalf("a dev build must not claim an upgrade: %s", h)
	}
}

func TestProbeHealthReadsTheServerVersion(t *testing.T) {
	serve := func(body string, code int) int {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			fmt.Fprint(w, body)
		}))
		t.Cleanup(srv.Close)
		_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
		n, _ := strconv.Atoi(p)
		return n
	}
	if v, ok := probeHealth(serve(`{"status":"ok","service":"engram","version":"1.21.0"}`, 200)); !ok || v != "1.21.0" {
		t.Fatalf("got %q %v", v, ok)
	}
	if _, ok := probeHealth(serve(`{"service":"other","version":"1"}`, 200)); ok {
		t.Fatal("a non-engram answer must not count")
	}
	if _, ok := probeHealth(serve(`not json`, 200)); ok {
		t.Fatal("garbage must not count")
	}
	if _, ok := probeHealth(serve(`{"service":"engram"}`, 500)); ok {
		t.Fatal("an error status must not count")
	}
}

// engram serve on a taken port, held by an older engram server, says so.
func TestServeOnATakenPortNamesTheOldServer(t *testing.T) {
	cfg := store.Config{DataDir: t.TempDir()}
	stubRuntimeHooks(t)
	stubExitWithPanic(t)
	oldVersion := version
	version = "1.22.0"
	t.Cleanup(func() { version = oldVersion })

	// A real older "server" on a free port, and a real address-in-use error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "service": "engram", "version": "1.21.0"})
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	t.Setenv("ENGRAM_PORT", p)

	withArgs(t, "engram", "serve")
	newHTTPServer = func(s *store.Store, port int) *engramsrv.Server { return engramsrv.New(s, port) }
	startHTTP = func(*engramsrv.Server) error {
		_, err := net.Listen("tcp", srv.Listener.Addr().String())
		if err == nil {
			t.Fatal("expected address in use")
		}
		if !errors.Is(err, syscall.EADDRINUSE) && !isAddrInUse(err) {
			t.Fatalf("not recognised as address in use: %v", err)
		}
		return err
	}
	_, stderr, rec := captureOutputAndRecover(t, func() { cmdServe(cfg) })
	if _, ok := rec.(exitCode); !ok {
		t.Fatalf("serve should exit 1, got %v", rec)
	}
	for _, want := range []string{"1.21.0", "1.22.0", "old `engram serve`", "Stop it"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr lacks %q: %s", want, stderr)
		}
	}
}
