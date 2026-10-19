package server

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"

	"github.com/Gentleman-Programming/engram/internal/portowner"
)

// The HTTP port is shared by every OS user of the machine, and most routes have
// no authentication. As defense in depth, a request whose client socket is
// positively identified as owned by another OS user is refused (403) on every
// route but /health. Anything undeterminable fails open.

// envAllowOtherUsers set to "1" disables the check.
const envAllowOtherUsers = "ENGRAM_ALLOW_OTHER_USERS"

type ctxKey struct{}

// connOwner resolves, once per connection, the uid behind the client socket.
type connOwner struct {
	client, server netip.AddrPort
	paths          func() portowner.Paths

	once sync.Once
	uid  int
	err  error
}

func (c *connOwner) resolve() (int, error) {
	c.once.Do(func() {
		c.uid, c.err = portowner.ConnUID(c.client, c.server, c.paths())
	})
	return c.uid, c.err
}

// connContext is the http.Server.ConnContext hook: it records the connection's
// addresses so the guard can look the client up lazily (and cache the answer
// for the lifetime of a keep-alive connection).
func (s *Server) connContext(ctx context.Context, c net.Conn) context.Context {
	client, ok1 := addrPort(c.RemoteAddr())
	server, ok2 := addrPort(c.LocalAddr())
	if !ok1 || !ok2 || !client.Addr().IsLoopback() {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, &connOwner{client: client, server: server, paths: s.procPaths})
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
		if err != nil || uid < 0 || uid == self || uid == 0 {
			next.ServeHTTP(w, r)
			return
		}
		jsonErrorWithFields(w, http.StatusForbidden,
			"this engram belongs to another OS user; use your own (see `engram port`)",
			map[string]any{"code": "foreign_uid"})
	})
}
