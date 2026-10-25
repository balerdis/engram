// Package portowner decides which TCP port an OS user's engram talks to and who
// owns a loopback connection, reading /proc/net/tcp and /proc/net/tcp6.
//
// engram's HTTP server listens on 127.0.0.1:7437 and that port is shared by every
// OS user of the machine. The rule here is the one Pegasus Harness already ships
// on the client side (same env names, same formula, same strict parsing):
//
//   - ENGRAM_PORT set to a valid port (1-5 digits, 1..65535): that port when
//     nobody or this uid listens on it, otherwise nothing (no fallback);
//   - otherwise 7437 when nobody or this uid listens on it;
//   - otherwise 17437 + uid % 10000 under the same rule, else nothing;
//   - without /proc/net/tcp it cannot tell and behaves as before.
//
// Every way of not knowing (no /proc, unreadable or corrupt table, non-Linux)
// answers "nobody" or an error, and the callers fail open.
package portowner

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	// DefaultPort is engram's historical port.
	DefaultPort = 7437
	// FallbackBase and FallbackSpan give the per-user port 17437 + uid % 10000.
	FallbackBase = 17437
	FallbackSpan = 10000

	// Test seams, honored by every implementation of the rule.
	EnvTCP4         = "ENGRAM_PORT_PROC_TCP4"
	EnvTCP6         = "ENGRAM_PORT_PROC_TCP6"
	EnvFallbackBase = "ENGRAM_PORT_FALLBACK_BASE"
	EnvPort         = "ENGRAM_PORT"

	procTCP4 = "/proc/net/tcp"
	procTCP6 = "/proc/net/tcp6"

	stateListen = "0A"
)

// Addresses whose listener blocks a bind on 127.0.0.1, as /proc prints them:
// loopback itself and the wildcard of a service listening on every interface.
var (
	loopbackV4 = []string{"0100007F", "00000000"}
	loopbackV6 = []string{
		"00000000000000000000000001000000",
		"0000000000000000FFFF00000100007F",
		"00000000000000000000000000000000",
		"0000000000000000FFFF000000000000",
	}

	portText = regexp.MustCompile(`^[0-9]{1,5}$`)
	hexPort  = regexp.MustCompile(`^[0-9a-fA-F]{1,8}$`)
	uidText  = regexp.MustCompile(`^[0-9]{1,10}$`)
)

// Paths are the two tables to read.
type Paths struct{ TCP4, TCP6 string }

// PathsFromEnv honors the test seams and defaults to the real /proc.
func PathsFromEnv(getenv func(string) string) Paths {
	p := Paths{TCP4: procTCP4, TCP6: procTCP6}
	if v := getenv(EnvTCP4); v != "" {
		p.TCP4 = v
	}
	if v := getenv(EnvTCP6); v != "" {
		p.TCP6 = v
	}
	return p
}

// Available reports whether the IPv4 table exists, the condition for telling anything.
func (p Paths) Available() bool {
	st, err := os.Stat(p.TCP4)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(p.TCP4)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

// readLines returns the lines of a table. An unopenable file yields no lines and
// no error (the table does not exist for us); a read error mid-file is returned
// so callers can tell "empty" from "cut short".
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

// ListenUIDs returns the uids of the sockets LISTENing on port at one of loopbacks.
// The first line is a header and is always skipped.
func ListenUIDs(path string, port int, loopbacks []string) map[int]bool {
	uids := map[int]bool{}
	lines, _ := readLines(path)
	if len(lines) == 0 {
		return uids
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 8 || f[3] != stateListen {
			continue
		}
		addr, hexp, ok := strings.Cut(f[1], ":")
		if !ok || !contains(loopbacks, strings.ToUpper(addr)) {
			continue
		}
		if !hexPort.MatchString(hexp) || !uidText.MatchString(f[7]) {
			continue
		}
		n, err := strconv.ParseUint(hexp, 16, 64)
		if err != nil || n != uint64(port) {
			continue
		}
		uid, err := strconv.Atoi(f[7])
		if err != nil {
			continue
		}
		uids[uid] = true
	}
	return uids
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Holders returns every uid listening on port over IPv4 or IPv6 loopback (or the wildcard).
func Holders(port int, p Paths) []int {
	set := ListenUIDs(p.TCP4, port, loopbackV4)
	for uid := range ListenUIDs(p.TCP6, port, loopbackV6) {
		set[uid] = true
	}
	out := make([]int, 0, len(set))
	for uid := range set {
		out = append(out, uid)
	}
	sort.Ints(out)
	return out
}

// FallbackPort is the per-user port, 17437 + uid % 10000. The base can be moved
// with ENGRAM_PORT_FALLBACK_BASE as long as the result stays a valid port.
func FallbackPort(uid int, getenv func(string) string) int {
	base := FallbackBase
	if raw := strings.TrimSpace(getenv(EnvFallbackBase)); portText.MatchString(raw) {
		if n, _ := strconv.Atoi(raw); n >= 1 && n+uid%FallbackSpan <= 65535 {
			base = n
		}
	}
	return base + uid%FallbackSpan
}

// ExplicitPort returns ENGRAM_PORT when it is a valid port; an unusable value counts as unset.
func ExplicitPort(getenv func(string) string) (int, bool) {
	raw := strings.TrimSpace(getenv(EnvPort))
	if !portText.MatchString(raw) {
		return 0, false
	}
	n, _ := strconv.Atoi(raw)
	if n < 1 || n > 65535 {
		return 0, false
	}
	return n, true
}

// Resolution is the outcome of Resolve.
type Resolution struct {
	// Port is the port to use, or 0 when every candidate is held by another uid.
	Port int
	// BlockedPort is the port that was refused (0 when nothing was), BlockedUIDs who holds it.
	BlockedPort int
	BlockedUIDs []int
	Explicit    bool
}

// Resolve applies the rule for uid. A negative uid (Windows) cannot be compared,
// so it behaves as if /proc were unavailable.
func Resolve(getenv func(string) string, uid int, p Paths) Resolution {
	explicit, hasExplicit := ExplicitPort(getenv)
	if uid < 0 || !p.Available() {
		if hasExplicit {
			return Resolution{Port: explicit, Explicit: true}
		}
		return Resolution{Port: DefaultPort}
	}
	usable := func(port int) bool {
		h := Holders(port, p)
		if len(h) == 0 {
			return true
		}
		for _, u := range h {
			if u == uid {
				return true
			}
		}
		return false
	}
	if hasExplicit {
		if usable(explicit) {
			return Resolution{Port: explicit, Explicit: true}
		}
		return Resolution{BlockedPort: explicit, BlockedUIDs: Holders(explicit, p), Explicit: true}
	}
	if usable(DefaultPort) {
		return Resolution{Port: DefaultPort}
	}
	refused := Holders(DefaultPort, p)
	if fb := FallbackPort(uid, getenv); usable(fb) {
		return Resolution{Port: fb, BlockedPort: DefaultPort, BlockedUIDs: refused}
	}
	return Resolution{BlockedPort: DefaultPort, BlockedUIDs: refused}
}

// Explain says why the resolution has no port (empty when it has one and nothing was refused).
func (r Resolution) Explain(uid int, getenv func(string) string) string {
	if r.BlockedPort == 0 {
		return ""
	}
	owners := make([]string, len(r.BlockedUIDs))
	for i, u := range r.BlockedUIDs {
		owners[i] = strconv.Itoa(u)
	}
	held := fmt.Sprintf("port %d on 127.0.0.1 is held by a process of another OS user (uid %s)", r.BlockedPort, strings.Join(owners, ", "))
	switch {
	case r.Port != 0:
		return fmt.Sprintf("%s; using port %d, derived from this user's uid", held, r.Port)
	case r.Explicit:
		return fmt.Sprintf("%s and ENGRAM_PORT points at it: engram memory is disabled until ENGRAM_PORT is set to a free port", held)
	default:
		return fmt.Sprintf("%s, and so is the port derived from this user's uid (%d): engram memory is disabled until ENGRAM_PORT is set to a free port", held, FallbackPort(uid, getenv))
	}
}

// procAddr renders an address the way /proc/net/tcp{,6} prints it: every 32-bit
// word as native-endian (little-endian) hex.
func procAddr(a netip.Addr) string {
	b := a.AsSlice()
	var sb strings.Builder
	for i := 0; i+4 <= len(b); i += 4 {
		fmt.Fprintf(&sb, "%02X%02X%02X%02X", b[i+3], b[i+2], b[i+1], b[i])
	}
	return sb.String()
}

func procEndpoint(a netip.AddrPort) string {
	return fmt.Sprintf("%s:%04X", procAddr(a.Addr().Unmap()), a.Port())
}

func procEndpoint6(a netip.AddrPort) string {
	addr := a.Addr()
	if addr.Is4() {
		addr = netip.AddrFrom16(addr.As16())
	}
	return fmt.Sprintf("%s:%04X", procAddr(addr), a.Port())
}

// ErrUnavailable means /proc/net/tcp does not exist or cannot be opened (non-Linux,
// restricted /proc): the owner cannot be told and callers fail open.
var ErrUnavailable = errors.New("portowner: socket table unavailable")

// ErrNoRow means the table is readable but holds no ESTABLISHED row for the client
// (the client already closed, or the table could not be read completely): callers
// that guard something must fail closed.
var ErrNoRow = errors.New("portowner: no established socket row for the client")

const stateEstablished = "01"

// ConnUID finds the uid of the process that owns the client end of a loopback
// connection: the ESTABLISHED row whose local address is the client's and whose
// remote address is the server's. Rows in any other state (FIN_WAIT, TIME_WAIT...)
// are never trusted: closed sockets show uid 0 there. It returns ErrUnavailable
// when /proc cannot be used and ErrNoRow (possibly wrapped) when it can but the
// client has no ESTABLISHED row.
func ConnUID(client, server netip.AddrPort, p Paths) (int, error) {
	if !p.Available() {
		return -1, ErrUnavailable
	}
	type probe struct {
		path, local, remote string
	}
	var probes []probe
	if client.Addr().Unmap().Is4() && server.Addr().Unmap().Is4() {
		probes = append(probes, probe{p.TCP4, procEndpoint(client), procEndpoint(server)})
	}
	probes = append(probes, probe{p.TCP6, procEndpoint6(client), procEndpoint6(server)})
	var scanErr error
	for _, pr := range probes {
		uid, found, err := scanEstablished(pr.path, pr.local, pr.remote)
		if err != nil {
			scanErr = err
		}
		if found {
			return uid, nil
		}
	}
	if scanErr != nil {
		return -1, fmt.Errorf("%w for %s -> %s (read error: %v)", ErrNoRow, client, server, scanErr)
	}
	return -1, fmt.Errorf("%w for %s -> %s", ErrNoRow, client, server)
}

// scanEstablished streams a table and stops at the first ESTABLISHED row going
// from local to remote, so a match early in the table does not pay for the rest.
// An unopenable file counts as an empty table; a read error mid-file is returned.
func scanEstablished(path, local, remote string) (uid int, found bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false, nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || !strings.EqualFold(f[1], local) || !strings.EqualFold(f[2], remote) || f[3] != stateEstablished {
			continue
		}
		if !uidText.MatchString(f[7]) {
			continue
		}
		if n, convErr := strconv.Atoi(f[7]); convErr == nil {
			return n, true, nil
		}
	}
	return 0, false, sc.Err()
}
