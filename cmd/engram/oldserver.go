package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A server started by an older binary keeps running after the binary is
// replaced, and nothing says so. `engram serve` is where a new binary first
// meets it when the port is taken, so the failure explains who holds the port.
// (The Claude Code and Codex session-start hooks do the same comparison for
// the server they find already running.)

// healthBodyLimit caps what is read from /health: whatever listens on the port
// is not necessarily engram, and its text must not be unbounded.
const healthBodyLimit = 4096

// validVersion reports whether v looks like a version string. Only such a
// string is ever printed: /health is answered by whatever holds the port, and
// its version ends up in the terminal and in text a model reads, so anything
// else (newlines, escape bytes, a sentence) is dropped, never shown.
var validVersion = regexp.MustCompile(`^[0-9A-Za-z.+_-]{1,32}$`)

// probeHealth asks the engram server on 127.0.0.1:port for its version
// (/health is open to every user). ok is true only for an engram service with
// a well-formed version. Tests replace it.
var probeHealth = func(port int) (version string, ok bool) {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	var body struct {
		Service string `json:"service"`
		Version string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, healthBodyLimit)).Decode(&body) != nil || body.Service != "engram" {
		return "", false
	}
	if !validVersion.MatchString(body.Version) {
		return "", false
	}
	return body.Version, true
}

// versionLess reports whether dotted version a is older than b. Only plain
// numeric dotted versions are compared (an optional leading "v" and any
// pre-release or build suffix after "-" or "+" are ignored); ok is false when
// either side is not one.
func versionLess(a, b string) (less, ok bool) {
	pa, oka := versionParts(a)
	pb, okb := versionParts(b)
	if !oka || !okb {
		return false, false
	}
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			return x < y, true
		}
	}
	return false, true
}

func versionParts(v string) ([]int, bool) {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, len(out) > 0
}

func isAddrInUse(err error) bool {
	// 10048 is WSAEADDRINUSE on Windows.
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048)) || strings.Contains(err.Error(), "address already in use")
}

// busyPortHint explains an address-in-use failure of `engram serve` when an
// engram server holds the port: empty when something else does. The old-server
// claim is made only when that server is OLDER than this binary; a newer, equal,
// dev or unparsable one gets a neutral line.
func busyPortHint(port int) string {
	ver, ok := probeHealth(port)
	if !ok {
		return ""
	}
	if older, cmp := versionLess(ver, version); cmp && older {
		return fmt.Sprintf("an engram server of version %s is already running on port %d, but this binary is %s: an old `engram serve` kept running after the upgrade. Stop it (find it with `pgrep -af \"engram serve\"`, then `kill <pid>`) and start this one again", ver, port, version)
	}
	return fmt.Sprintf("an engram server (version %s) is already running on port %d: nothing to start", orDev(ver), port)
}

func orDev(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
