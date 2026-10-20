//go:build !windows

package plugin_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The Windows-native prompt hook, run under PowerShell for Linux when `pwsh` is
// on PATH (the test is skipped otherwise). It exercises the logic (port
// resolution, disabled, payload encoding); Windows-only behavior (engram.exe
// resolution, console encodings) is not covered.
func TestPowerShellPromptHook(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh not available")
	}
	run := func(mode, stdin string) (posts []string, out string) {
		var mu sync.Mutex
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			posts = append(posts, r.URL.Path+" "+r.Header.Get("Content-Type")+" "+string(b))
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()
		_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
		bin := t.TempDir()
		fake := "#!/bin/sh\ncase \"$FAKE_MODE\" in\n ok) [ \"$1\" = port ] && echo \"$FAKE_PORT\" ;;\n disabled) echo 'memory is disabled' >&2; exit 1 ;;\n garbage) echo hello ;;\nesac\n"
		if err := os.WriteFile(filepath.Join(bin, "engram"), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
		home := t.TempDir()
		cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", filepath.Join(repoRoot(t), "plugin", "claude-code", "scripts", "user-prompt-submit.ps1"))
		cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + home, "DOTNET_CLI_HOME=" + home,
			"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1", "POWERSHELL_TELEMETRY_OPTOUT=1", "POWERSHELL_UPDATECHECK=Off",
			"FAKE_MODE=" + mode, "FAKE_PORT=" + port, "ENGRAM_PORT=" + port}
		cmd.Stdin = strings.NewReader(stdin)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("hook must exit 0: %v\n%s", err, b)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), posts...), string(b)
	}
	posts, out := run("ok", `{"session_id":"s1","prompt":"café 你好 \"q\""}`)
	if out != "" || len(posts) != 1 || posts[0] != "/prompts application/json; charset=utf-8 "+`{"session_id":"s1","content":"café 你好 \"q\""}` {
		t.Fatalf("ok: out=%q posts=%q", out, posts)
	}
	for name, c := range map[string][2]string{
		"blank prompt": {"ok", `{"session_id":"s1","prompt":"  "}`},
		"no session":   {"ok", `{"session_id":"","prompt":"x"}`},
		"not json":     {"ok", `not json`},
		"disabled":     {"disabled", `{"session_id":"s1","prompt":"x"}`},
		"garbage port": {"garbage", `{"session_id":"s1","prompt":"x"}`},
	} {
		if posts, out := run(c[0], c[1]); len(posts) != 0 || out != "" {
			t.Fatalf("%s: must send and print nothing, got %q %q", name, posts, out)
		}
	}
}
