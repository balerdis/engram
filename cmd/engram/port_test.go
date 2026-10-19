package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	engramsrv "github.com/Gentleman-Programming/engram/internal/server"
	"github.com/Gentleman-Programming/engram/internal/store"
)

const procHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// fakeProc points the port rule at a synthetic table: a listener on port held by uid.
func fakeProc(t *testing.T, port, uid int) {
	t.Helper()
	dir := t.TempDir()
	tcp4 := filepath.Join(dir, "tcp")
	body := procHeader
	if uid >= 0 {
		body += fmt.Sprintf("   0: 0100007F:%04X 00000000:0000 0A 0:0 0:0 0 %d 0 1\n", port, uid)
	}
	if err := os.WriteFile(tcp4, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENGRAM_PORT_PROC_TCP4", tcp4)
	t.Setenv("ENGRAM_PORT_PROC_TCP6", filepath.Join(dir, "tcp6"))
	t.Setenv("ENGRAM_PORT_FALLBACK_BASE", "")
	t.Setenv("ENGRAM_PORT", "")
}

func runPort(t *testing.T) (stdout, stderr string, code int) {
	t.Helper()
	stubExitWithPanic(t)
	withArgs(t, "engram", "port")
	out, errOut, rec := captureOutputAndRecover(t, cmdPort)
	if c, ok := rec.(exitCode); ok {
		code = int(c)
	} else if rec != nil {
		t.Fatalf("panic: %v", rec)
	}
	return out, errOut, code
}

func TestCmdPortOutputAndExitCodes(t *testing.T) {
	own, foreign := os.Getuid(), os.Getuid()+1

	t.Run("free", func(t *testing.T) {
		fakeProc(t, 7437, -1)
		out, errOut, code := runPort(t)
		if out != "7437\n" || errOut != "" || code != 0 {
			t.Fatalf("%q %q %d", out, errOut, code)
		}
	})
	t.Run("own", func(t *testing.T) {
		fakeProc(t, 7437, own)
		if out, _, code := runPort(t); out != "7437\n" || code != 0 {
			t.Fatalf("%q %d", out, code)
		}
	})
	t.Run("foreign falls back, stdout is only the number", func(t *testing.T) {
		fakeProc(t, 7437, foreign)
		t.Setenv("ENGRAM_PORT_FALLBACK_BASE", "20000")
		out, _, code := runPort(t)
		if want := fmt.Sprintf("%d\n", 20000+own%10000); out != want || code != 0 {
			t.Fatalf("%q want %q (%d)", out, want, code)
		}
	})
	t.Run("explicit foreign disables memory", func(t *testing.T) {
		fakeProc(t, 9100, foreign)
		t.Setenv("ENGRAM_PORT", "9100")
		out, errOut, code := runPort(t)
		if out != "" || code != 1 || !strings.Contains(errOut, "ENGRAM_PORT") || !strings.Contains(errOut, "disabled") {
			t.Fatalf("%q %q %d", out, errOut, code)
		}
	})
	t.Run("both foreign disables memory", func(t *testing.T) {
		dir := t.TempDir()
		fb := 20000 + own%10000
		tcp4 := filepath.Join(dir, "tcp")
		body := procHeader +
			fmt.Sprintf("   0: 0100007F:%04X 00000000:0000 0A 0:0 0:0 0 %d 0 1\n", 7437, foreign) +
			fmt.Sprintf("   1: 0100007F:%04X 00000000:0000 0A 0:0 0:0 0 %d 0 1\n", fb, foreign)
		_ = os.WriteFile(tcp4, []byte(body), 0o600)
		t.Setenv("ENGRAM_PORT_PROC_TCP4", tcp4)
		t.Setenv("ENGRAM_PORT_PROC_TCP6", filepath.Join(dir, "none"))
		t.Setenv("ENGRAM_PORT", "")
		t.Setenv("ENGRAM_PORT_FALLBACK_BASE", "20000")
		out, errOut, code := runPort(t)
		if out != "" || code != 1 || errOut == "" {
			t.Fatalf("%q %q %d", out, errOut, code)
		}
	})
	t.Run("no /proc behaves as before", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT_PROC_TCP4", filepath.Join(t.TempDir(), "missing"))
		t.Setenv("ENGRAM_PORT", "8123")
		if out, _, code := runPort(t); out != "8123\n" || code != 0 {
			t.Fatalf("%q %d", out, code)
		}
	})
}

func TestPortSkipsUpdateCheck(t *testing.T) {
	if shouldCheckForUpdates([]string{"port"}) {
		t.Fatal("`engram port` must stay offline and script-friendly")
	}
}

func TestServeResolvesPort(t *testing.T) {
	own, foreign := os.Getuid(), os.Getuid()+1
	cfg := store.Config{DataDir: t.TempDir()}
	serve := func(t *testing.T, args ...string) (seen int, stderr string, fataled bool) {
		t.Helper()
		stubRuntimeHooks(t)
		stubExitWithPanic(t)
		withArgs(t, append([]string{"engram", "serve"}, args...)...)
		seen = -1
		newHTTPServer = func(s *store.Store, port int) *engramsrv.Server {
			seen = port
			return engramsrv.New(s, 0)
		}
		startHTTP = func(*engramsrv.Server) error { return nil }
		_, stderr, rec := captureOutputAndRecover(t, func() { cmdServe(cfg) })
		_, fataled = rec.(exitCode)
		return
	}

	t.Run("foreign default serves on the fallback", func(t *testing.T) {
		fakeProc(t, 7437, foreign)
		t.Setenv("ENGRAM_PORT_FALLBACK_BASE", "20000")
		if seen, _, fatal := serve(t); fatal || seen != 20000+own%10000 {
			t.Fatalf("seen %d fatal %v", seen, fatal)
		}
	})
	t.Run("own default is kept", func(t *testing.T) {
		fakeProc(t, 7437, own)
		if seen, _, fatal := serve(t); fatal || seen != 7437 {
			t.Fatalf("seen %d", seen)
		}
	})
	t.Run("explicit foreign port is refused", func(t *testing.T) {
		fakeProc(t, 9100, foreign)
		t.Setenv("ENGRAM_PORT", "9100")
		if seen, stderr, fatal := serve(t); !fatal || seen != -1 || !strings.Contains(stderr, "another OS user") {
			t.Fatalf("seen %d fatal %v %q", seen, fatal, stderr)
		}
	})
	t.Run("port argument foreign is refused", func(t *testing.T) {
		fakeProc(t, 9200, foreign)
		if seen, _, fatal := serve(t, "9200"); !fatal || seen != -1 {
			t.Fatalf("seen %d fatal %v", seen, fatal)
		}
	})
	t.Run("port argument free wins over env", func(t *testing.T) {
		fakeProc(t, 9200, -1)
		t.Setenv("ENGRAM_PORT", "9100")
		if seen, _, fatal := serve(t, "9300"); fatal || seen != 9300 {
			t.Fatalf("seen %d", seen)
		}
	})
	t.Run("no /proc keeps today's behavior", func(t *testing.T) {
		t.Setenv("ENGRAM_PORT_PROC_TCP4", filepath.Join(t.TempDir(), "missing"))
		t.Setenv("ENGRAM_PORT", "")
		if seen, _, fatal := serve(t); fatal || seen != 7437 {
			t.Fatalf("seen %d", seen)
		}
	})
}
