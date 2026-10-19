package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// A server started by an older binary keeps running after the binary is
// replaced, and nothing says so. `engram serve` is where a new binary first
// meets it when the port is taken, so the failure explains who holds the port.
// (The Claude Code and Codex session-start hooks do the same comparison for
// the server they find already running.)

// probeHealth asks the engram server on 127.0.0.1:port for its version
// (/health is open to every user). Tests replace it.
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
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || body.Service != "engram" {
		return "", false
	}
	return body.Version, true
}

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use")
}

// busyPortHint explains an address-in-use failure of `engram serve` when an
// engram server holds the port: empty when something else does.
func busyPortHint(port int) string {
	ver, ok := probeHealth(port)
	if !ok {
		return ""
	}
	switch {
	case ver == "" || ver == version:
		return fmt.Sprintf("an engram server (version %s) is already running on port %d: nothing to start", orDev(ver), port)
	case ver == "dev" || version == "dev":
		return fmt.Sprintf("an engram server (version %s) is already running on port %d; this binary is %s", ver, port, version)
	}
	return fmt.Sprintf("an engram server of version %s is already running on port %d, but this binary is %s: an old `engram serve` kept running after the upgrade. Stop it (find it with `pgrep -af \"engram serve\"`, then `kill <pid>`) and start this one again", ver, port, version)
}

func orDev(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
