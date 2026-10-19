package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"

	"github.com/Gentleman-Programming/engram/internal/portowner"
)

// The HTTP port is shared by every OS user of the machine, and most routes have
// no authentication. As defense in depth, a request whose client socket is
// positively identified as owned by another OS user is refused (403) on every
// route but /health. A loopback client that cannot be matched to an ESTABLISHED
// socket row while /proc is readable also counts as foreign (fail closed);
// only a missing/unreadable /proc, a non-loopback client or
// ENGRAM_ALLOW_OTHER_USERS=1 fail open.

// Known limit: a client that half-closes its side (shutdown(SHUT_WR) right after
// it sends the request, e.g. `nc -N`) can reach us after its FIN already arrived:
// the server-side row is then CLOSE_WAIT (08), not ESTABLISHED, so no row matches
// and, failing closed, the request gets 403 even from the right user. Ordinary
// HTTP clients (curl, browsers, the plugins) keep the write side open until they
// have the response and are not affected. Work around it by keeping the connection
// open until the response arrives (`nc -q 1`, curl) or, for a trusted machine, with
// ENGRAM_ALLOW_OTHER_USERS=1. /health is exempt.

// envAllowOtherUsers set to "1" disables the check.
const envAllowOtherUsers = "ENGRAM_ALLOW_OTHER_USERS"

type ctxKey struct{}

// connOwner holds the uid behind the client socket, resolved once per connection.
type connOwner struct {
	client, server netip.AddrPort
	uid            int
	err            error
}

func (c *connOwner) resolve() (int, error) { return c.uid, c.err }

// connContext is the http.Server.ConnContext hook. It runs right after Accept, while
// the client's socket is still ESTABLISHED, and resolves the owner there: a client
// that sends a request and closes at once would otherwise leave a FIN_WAIT/TIME_WAIT
// row (uid 0) or none by the time the first request is read. The answer is cached
// for the lifetime of a keep-alive connection.
func (s *Server) connContext(ctx context.Context, c net.Conn) context.Context {
	client, ok1 := addrPort(c.RemoteAddr())
	server, ok2 := addrPort(c.LocalAddr())
	if !ok1 || !ok2 || !client.Addr().IsLoopback() {
		return ctx
	}
	o := &connOwner{client: client, server: server}
	o.uid, o.err = portowner.ConnUID(client, server, s.procPaths())
	return context.WithValue(ctx, ctxKey{}, o)
}

func addrPort(a net.Addr) (netip.AddrPort, bool) {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}, false
	}
	ap := tcp.AddrPort()
	return ap, ap.IsValid()
}

func (s *Server) procPaths() portowner.Paths {
	return portowner.PathsFromEnv(os.Getenv)
}

// requestUID is the default resolver: the uid recorded for the request's connection.
func requestUID(r *http.Request) (int, error) {
	if c, ok := r.Context().Value(ctxKey{}).(*connOwner); ok {
		return c.resolve()
	}
	return -1, errNoConn
}

type constError string

func (e constError) Error() string { return string(e) }

const errNoConn = constError("no connection owner recorded")

// ownerGuard refuses clients owned by another OS user on every route except /health.
func (s *Server) ownerGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" || os.Getenv(envAllowOtherUsers) == "1" {
			next.ServeHTTP(w, r)
			return
		}
		self := s.selfUID()
		if self < 0 {
			next.ServeHTTP(w, r)
			return
		}
		uid, err := s.uidOf(r)
		if errors.Is(err, portowner.ErrUnavailable) || errors.Is(err, errNoConn) || (err == nil && (uid == self || uid == 0)) {
			next.ServeHTTP(w, r)
			return
		}
		if err == nil && uid < 0 {
			next.ServeHTTP(w, r)
			return
		}
		jsonErrorWithFields(w, http.StatusForbidden,
			"this engram belongs to another OS user; use your own (see `engram port`)",
			map[string]any{"code": "foreign_uid"})
	})
}
