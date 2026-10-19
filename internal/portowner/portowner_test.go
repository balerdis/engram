package portowner

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	ownUID     = 4242
	foreignUID = 4243
	header     = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func row(i int, addr string, port int, state string, uid int) string {
	return fmt.Sprintf("  %2d: %s:%04X 00000000:0000 %s 00000000:00000000 00:00000000 00000000 %5d        0 %d 1 0000000000000000 100 0 0 10 0\n",
		i, addr, port, state, uid, 1000+i)
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListenUIDsTable(t *testing.T) {
	cases := []struct {
		name  string
		table string
		lb    []string
		want  []int
	}{
		{"own", header + row(0, "0100007F", 7437, "0A", ownUID), loopbackV4, []int{ownUID}},
		{"foreign", header + row(0, "0100007F", 7437, "0A", foreignUID), loopbackV4, []int{foreignUID}},
		{"mixed", header + row(0, "0100007F", 7437, "0A", foreignUID) + row(1, "00000000", 7437, "0A", ownUID), loopbackV4, []int{ownUID, foreignUID}},
		{"tcp6 loopback", header + row(0, "00000000000000000000000001000000", 7437, "0A", foreignUID), loopbackV6, []int{foreignUID}},
		{"tcp6 wildcard", header + row(0, "00000000000000000000000000000000", 7437, "0A", foreignUID), loopbackV6, []int{foreignUID}},
		{"tcp6 mapped", header + row(0, "0000000000000000FFFF00000100007F", 7437, "0A", foreignUID), loopbackV6, []int{foreignUID}},
		{"time wait ignored", header + row(0, "0100007F", 7437, "06", foreignUID), loopbackV4, nil},
		{"established ignored", header + row(0, "0100007F", 7437, "01", foreignUID), loopbackV4, nil},
		{"other interface", header + row(0, "0A00000A", 7437, "0A", foreignUID), loopbackV4, nil},
		{"other port", header + row(0, "0100007F", 7438, "0A", foreignUID), loopbackV4, nil},
		{"no header loses first row", row(0, "0100007F", 7437, "0A", foreignUID), loopbackV4, nil},
		{"corrupt line skipped", header + "garbage\n" + "  0: 0100007F:ZZZZ 00000000:0000 0A a b c d 5\n" + row(2, "0100007F", 7437, "0A", ownUID), loopbackV4, []int{ownUID}},
		{"uid must be digits", header + "  0: 0100007F:1D0D 00000000:0000 0A 0:0 0:0 0 +5 0 1\n", loopbackV4, nil},
		{"empty file", "", loopbackV4, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ListenUIDs(write(t, "tcp", c.table), 7437, c.lb)
			if len(got) != len(c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
			for _, u := range c.want {
				if !got[u] {
					t.Fatalf("got %v want %v", got, c.want)
				}
			}
		})
	}
	if got := ListenUIDs(filepath.Join(t.TempDir(), "missing"), 7437, loopbackV4); len(got) != 0 {
		t.Fatal("missing file must hold nobody")
	}
}

func TestFallbackFormula(t *testing.T) {
	none := envOf(nil)
	for uid, want := range map[int]int{0: 17437, 1000: 18437, 9999: 27436, 10000: 17437, 65534: 17437 + 5534} {
		if got := FallbackPort(uid, none); got != want {
			t.Errorf("uid %d: %d want %d", uid, got, want)
		}
	}
	if got := FallbackPort(1000, envOf(map[string]string{EnvFallbackBase: "20000"})); got != 21000 {
		t.Errorf("base seam: %d", got)
	}
	if got := FallbackPort(9999, envOf(map[string]string{EnvFallbackBase: "60000"})); got != 27436 {
		t.Errorf("overflowing base must be ignored: %d", got)
	}
}

func TestExplicitPort(t *testing.T) {
	for in, want := range map[string]int{"9999": 9999, " 80 ": 80, "65535": 65535} {
		if got, ok := ExplicitPort(envOf(map[string]string{EnvPort: in})); !ok || got != want {
			t.Errorf("%q: %d %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "0", "70000", "+5", "-1", "1_0", "007437", "abc", strings.Repeat("9", 400)} {
		if _, ok := ExplicitPort(envOf(map[string]string{EnvPort: in})); ok {
			t.Errorf("%q must count as unset", in)
		}
	}
}

type vector struct {
	Name         string            `json:"name"`
	EnvPort      *string           `json:"env_port"`
	TCP4         []json.RawMessage `json:"tcp4"`
	TCP6         []json.RawMessage `json:"tcp6"`
	Expect       json.RawMessage   `json:"expect"`
	Header       *bool             `json:"header"`
	FallbackBase string            `json:"fallback_base"`
}

func token(raw json.RawMessage, uid int, base int) int {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case "default":
			return DefaultPort
		case "fallback":
			return base + uid%FallbackSpan
		}
		n, _ := strconv.Atoi(s)
		return n
	}
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}

func table(t *testing.T, rows []json.RawMessage, withHeader bool) string {
	var sb strings.Builder
	if withHeader {
		sb.WriteString(header)
	}
	for i, r := range rows {
		var verbatim string
		if json.Unmarshal(r, &verbatim) == nil {
			sb.WriteString(verbatim + "\n")
			continue
		}
		var cells []json.RawMessage
		if err := json.Unmarshal(r, &cells); err != nil || len(cells) != 4 {
			t.Fatalf("bad row %s", r)
		}
		var addr, state, owner string
		_ = json.Unmarshal(cells[0], &addr)
		_ = json.Unmarshal(cells[2], &state)
		_ = json.Unmarshal(cells[3], &owner)
		uid := foreignUID
		if owner == "own" {
			uid = ownUID
		}
		sb.WriteString(row(i, addr, token(cells[1], ownUID, FallbackBase), state, uid))
	}
	return sb.String()
}

// TestParityVectors feeds the Pegasus Harness vectors to the Go resolver: the
// cross-repo check that both sides of the port resolution agree.
func TestParityVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/engram_port_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Vectors) < 35 {
		t.Fatalf("only %d vectors", len(doc.Vectors))
	}
	for _, v := range doc.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			dir := t.TempDir()
			env := map[string]string{
				EnvTCP4: filepath.Join(dir, "tcp"),
				EnvTCP6: filepath.Join(dir, "tcp6"),
			}
			withHeader := v.Header == nil || *v.Header
			if v.TCP4 != nil {
				if err := os.WriteFile(env[EnvTCP4], []byte(table(t, v.TCP4, withHeader)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if v.TCP6 != nil {
				if err := os.WriteFile(env[EnvTCP6], []byte(table(t, v.TCP6, withHeader)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if v.EnvPort != nil {
				env[EnvPort] = *v.EnvPort
			}
			base := FallbackBase
			if v.FallbackBase != "" {
				env[EnvFallbackBase] = v.FallbackBase
				base, _ = strconv.Atoi(v.FallbackBase)
			}
			want := 0
			if string(v.Expect) != "null" {
				want = token(v.Expect, ownUID, base)
			}
			got := Resolve(envOf(env), ownUID, PathsFromEnv(envOf(env)))
			if got.Port != want {
				t.Fatalf("port %d, want %d (%+v)", got.Port, want, got)
			}
		})
	}
}

func TestResolveWithoutProcOrUID(t *testing.T) {
	missing := Paths{TCP4: filepath.Join(t.TempDir(), "nope"), TCP6: "x"}
	if r := Resolve(envOf(nil), ownUID, missing); r.Port != DefaultPort {
		t.Fatal(r)
	}
	if r := Resolve(envOf(map[string]string{EnvPort: "9000"}), ownUID, missing); r.Port != 9000 || !r.Explicit {
		t.Fatal(r)
	}
	held := Paths{TCP4: write(t, "tcp", header+row(0, "0100007F", 7437, "0A", foreignUID)), TCP6: "x"}
	if r := Resolve(envOf(nil), -1, held); r.Port != DefaultPort {
		t.Fatal("negative uid cannot be compared:", r)
	}
}

func TestExplain(t *testing.T) {
	dir := t.TempDir()
	tcp4 := filepath.Join(dir, "tcp")
	env := envOf(map[string]string{})
	_ = os.WriteFile(tcp4, []byte(header+row(0, "0100007F", 7437, "0A", foreignUID)), 0o600)
	p := Paths{TCP4: tcp4, TCP6: "x"}
	r := Resolve(env, ownUID, p)
	if r.Port != FallbackBase+ownUID%FallbackSpan || !strings.Contains(r.Explain(ownUID, env), "using port") {
		t.Fatal(r, r.Explain(ownUID, env))
	}
	envE := envOf(map[string]string{EnvPort: "7437"})
	r = Resolve(envE, ownUID, p)
	if r.Port != 0 || !strings.Contains(r.Explain(ownUID, envE), "ENGRAM_PORT points at it") {
		t.Fatal(r, r.Explain(ownUID, envE))
	}
	if (Resolution{Port: 7437}).Explain(ownUID, env) != "" {
		t.Fatal("nothing refused, nothing to explain")
	}
}

func TestProcAddr(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1": "0100007F",
		"::1":       "00000000000000000000000001000000",
		"::":        "00000000000000000000000000000000",
	}
	for in, want := range cases {
		if got := procAddr(netip.MustParseAddr(in)); got != want {
			t.Errorf("%s: %s want %s", in, got, want)
		}
	}
	if got := procEndpoint6(netip.MustParseAddrPort("127.0.0.1:80")); got != "0000000000000000FFFF00000100007F:0050" {
		t.Errorf("mapped: %s", got)
	}
}

func TestConnUID(t *testing.T) {
	client := netip.MustParseAddrPort("127.0.0.1:54321")
	server := netip.MustParseAddrPort("127.0.0.1:7437")
	line := func(l, r string, uid int) string {
		return fmt.Sprintf("   0: %s %s 01 00000000:00000000 00:00000000 00000000 %5d 0 1 1 0 100 0 0 10 0\n", l, r, uid)
	}
	t4 := write(t, "tcp", header+
		line("0100007F:1D0D", "0100007F:D431", 1)+ // server side of the pair: not ours to read
		line("0100007F:D431", "0100007F:1D0D", foreignUID))
	p := Paths{TCP4: t4, TCP6: filepath.Join(t.TempDir(), "none")}
	if uid, err := ConnUID(client, server, p); err != nil || uid != foreignUID {
		t.Fatalf("v4: %d %v", uid, err)
	}
	if _, err := ConnUID(netip.MustParseAddrPort("127.0.0.1:1"), server, p); err == nil {
		t.Fatal("row gone must error")
	}
	if _, err := ConnUID(client, server, Paths{TCP4: filepath.Join(t.TempDir(), "x")}); err == nil {
		t.Fatal("no /proc must error")
	}
	if _, err := ConnUID(netip.MustParseAddrPort("127.0.0.1:1"), server, p); !errors.Is(err, ErrNoRow) {
		t.Fatalf("want ErrNoRow, got %v", err)
	}
	if _, err := ConnUID(client, server, Paths{TCP4: filepath.Join(t.TempDir(), "x")}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	// Rows of closed sockets (FIN_WAIT2, TIME_WAIT, CLOSE_WAIT) show uid 0 and are never trusted.
	for _, st := range []string{"05", "06", "08", "04"} {
		closed := write(t, "tcp-"+st, header+strings.Replace(line("0100007F:D431", "0100007F:1D0D", 0), " 01 ", " "+st+" ", 1))
		if uid, err := ConnUID(client, server, Paths{TCP4: closed, TCP6: filepath.Join(t.TempDir(), "none")}); !errors.Is(err, ErrNoRow) {
			t.Fatalf("state %s: %d %v", st, uid, err)
		}
	}
	// uid 0 only from an ESTABLISHED row.
	root := write(t, "tcp-root", header+line("0100007F:D431", "0100007F:1D0D", 0))
	if uid, err := ConnUID(client, server, Paths{TCP4: root}); err != nil || uid != 0 {
		t.Fatalf("root established: %d %v", uid, err)
	}
	c6 := netip.MustParseAddrPort("[::1]:54321")
	s6 := netip.MustParseAddrPort("[::1]:7437")
	t6 := write(t, "tcp6", header+line("00000000000000000000000001000000:D431", "00000000000000000000000001000000:1D0D", ownUID))
	if uid, err := ConnUID(c6, s6, Paths{TCP4: t4, TCP6: t6}); err != nil || uid != ownUID {
		t.Fatalf("v6: %d %v", uid, err)
	}
}

// A table that cannot be read to the end is "readable but inconclusive": it must not
// look like a missing /proc (which fails open) nor like a clean answer.
func TestConnUIDScanErrorIsNotUnavailable(t *testing.T) {
	client := netip.MustParseAddrPort("127.0.0.1:54321")
	server := netip.MustParseAddrPort("127.0.0.1:7437")
	body := header + strings.Repeat("x", 2*1024*1024) + "\n"
	p := Paths{TCP4: write(t, "tcp-long", body), TCP6: filepath.Join(t.TempDir(), "none")}
	_, err := ConnUID(client, server, p)
	if !errors.Is(err, ErrNoRow) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("scan error must fail closed, got %v", err)
	}
	if _, err := readLines(p.TCP4); err == nil {
		t.Fatal("readLines must report the scan error")
	}
}
