package portowner

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkConnUID2000Rows measures one guard lookup against a 2,000-row table
// whose only matching row is the last one (worst case: full scan).
func BenchmarkConnUID2000Rows(b *testing.B) {
	client := netip.MustParseAddrPort("127.0.0.1:50000")
	server := netip.MustParseAddrPort("127.0.0.1:7437")
	var sb strings.Builder
	sb.WriteString("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	row := func(i int, local, remote string, uid int) {
		fmt.Fprintf(&sb, "%4d: %s %s 01 00000000:00000000 00:00000000 00000000  %5d        0 %d 1 0000000000000000 100 0 0 10 0\n", i, local, remote, uid, 1000+i)
	}
	for i := 0; i < 1999; i++ {
		row(i, fmt.Sprintf("0100007F:%04X", 20000+i), "0100007F:0050", 1000)
	}
	row(1999, procEndpoint(client), procEndpoint(server), 1000)
	dir := b.TempDir()
	t4 := filepath.Join(dir, "tcp")
	t6 := filepath.Join(dir, "tcp6")
	os.WriteFile(t4, []byte(sb.String()), 0o600)
	os.WriteFile(t6, []byte("header\n"), 0o600)
	p := Paths{TCP4: t4, TCP6: t6}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ConnUID(client, server, p); err != nil {
			b.Fatal(err)
		}
	}
}

func TestConnUIDStopsAtFirstEstablishedMatch(t *testing.T) {
	client := netip.MustParseAddrPort("127.0.0.1:50000")
	server := netip.MustParseAddrPort("127.0.0.1:7437")
	dir := t.TempDir()
	t4 := filepath.Join(dir, "tcp")
	line := func(state string, uid int) string {
		return fmt.Sprintf("   0: %s %s %s 00000000:00000000 00:00000000 00000000  %d        0 1 1 0\n", procEndpoint(client), procEndpoint(server), state, uid)
	}
	// A non-ESTABLISHED row never counts; the first ESTABLISHED one wins and
	// whatever follows (even garbage) is not read.
	body := "header\n" + line("06", 0) + line("01", 1234) + line("01", 99)
	if err := os.WriteFile(t4, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, err := ConnUID(client, server, Paths{TCP4: t4, TCP6: filepath.Join(dir, "none")})
	if err != nil || uid != 1234 {
		t.Fatalf("uid=%d err=%v", uid, err)
	}
}
