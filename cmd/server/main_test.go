package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amirashouri/tasktracker-server/internal/admin"
	"github.com/amirashouri/tasktracker-server/internal/auth"
	"github.com/amirashouri/tasktracker-server/internal/dbtest"
	"github.com/amirashouri/tasktracker-server/internal/entities"
	"github.com/amirashouri/tasktracker-server/internal/ratelimit"
	"github.com/amirashouri/tasktracker-server/internal/sync"
)

func TestHealthzDoesNotTouchTheDatabase(t *testing.T) {
	database := dbtest.NewDatabase(t)
	database.Close()

	rec := httptest.NewRecorder()
	healthz()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("liveness with a dead database = %d, want 200", rec.Code)
	}
}

func TestReadyzReportsTheDatabaseState(t *testing.T) {
	database := dbtest.NewDatabase(t)
	handler := readyz(database.DB)

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("readiness with a live database = %d, want 200", rec.Code)
	}

	database.Close()

	rec = httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readiness with a dead database = %d, want 503", rec.Code)
	}
}

// The mobile client names its sync scopes in the plural, and a scope is
// deliberately not an entity type: the `workItems` stream also carries project
// changes, because renaming a project has to reach a client that never asked
// for projects. The server shipped the singular paths, so every pull the app
// made was a 404 — and a 404 is invisible to a test that calls the handler
// directly instead of going through the route table.
func TestTheSyncPullPathsTheClientCallsResolve(t *testing.T) {
	mux := newTestMux(t)

	paths := []struct {
		name string
		path string
	}{
		{"the projects scope", "/v1/sync/changes/projects"},
		{"the workItems scope", "/v1/sync/changes/workItems"},
		{"the singular alias for projects", "/v1/sync/changes/project"},
		{"the singular alias for workItems", "/v1/sync/changes/workItem"},
	}

	for _, tc := range paths {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			// 401 is the point. It proves the path matched and reached the Auth
			// middleware; a 404 would mean nothing is registered at that path,
			// which is exactly the failure this test exists to catch.
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("GET %s = %d, want 401 (a 404 means the route is not registered)",
					tc.path, rec.Code)
			}
		})
	}
}

// A mux that answered everything would make the test above pass for the wrong
// reason, so an unregistered scope still has to 404.
func TestAnUnregisteredSyncScopeIsNotFound(t *testing.T) {
	mux := newTestMux(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sync/changes/sprints", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("GET an unregistered scope = %d, want 404", rec.Code)
	}
}

// The default bind is only useful if it is actually unreachable from the
// network, and the 0.0.0.0 opt-in is only useful if it is reachable. The test
// binds port 0 so it does not compete for a fixed port, and dials the host's
// own non-loopback address: the loopback check passes even when the bind is
// wrong, which is the mistake this test exists to catch.
func TestTheBindAddressControlsNetworkReachability(t *testing.T) {
	lan := nonLoopbackIPv4(t)

	for _, tc := range []struct {
		name         string
		bind         string
		lanReachable bool
	}{
		{"loopback", "127.0.0.1", false},
		{"all interfaces", "0.0.0.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", listenAddr(tc.bind, "0"))
			if err != nil {
				t.Fatalf("listen on %s: %v", tc.bind, err)
			}
			defer listener.Close()

			port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
			if err := dialTCP(net.JoinHostPort("127.0.0.1", port)); err != nil {
				t.Fatalf("loopback dial failed with SERVER_BIND=%s: %v", tc.bind, err)
			}

			addr := net.JoinHostPort(lan, port)
			err = dialTCP(addr)
			if tc.lanReachable && err != nil {
				t.Fatalf("LAN dial to %s failed with SERVER_BIND=%s: %v", addr, tc.bind, err)
			}
			if !tc.lanReachable && err == nil {
				t.Fatalf("LAN dial to %s succeeded with SERVER_BIND=%s; the server is reachable from the network", addr, tc.bind)
			}
		})
	}
}

func TestListenAddrBracketsIPv6(t *testing.T) {
	if got := listenAddr("::1", "8080"); got != "[::1]:8080" {
		t.Errorf("listenAddr(::1, 8080) = %q, want [::1]:8080", got)
	}
}

func dialTCP(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

// nonLoopbackIPv4 returns an address assigned to this host that is not
// loopback. A host with only loopback cannot exercise the distinction, so the
// test skips rather than inventing an address.
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("list interfaces: %v", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	t.Skip("no non-loopback IPv4 address to dial")
	return ""
}

func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()

	database := dbtest.NewDatabase(t)
	users := entities.NewUserRepository(database.DB)
	tokens := auth.NewTokenStore(database.DB)
	jwtManager := auth.NewJWTManager(strings.Repeat("s", 32), time.Hour, time.Hour)

	return mount(
		auth.NewHandler(jwtManager, users, tokens, ratelimit.New(5, time.Minute), nil),
		admin.NewHandler(users, tokens),
		admin.NewProjectHandler(entities.NewProjectRepository(database.DB), sync.NewEngine(database.DB, "")),
		sync.NewHandler(sync.NewEngine(database.DB, ""), jwtManager),
		jwtManager,
		database.DB,
		sync.NewEngine(database.DB, ""),
		"",
		int64(25*1024*1024),
	)
}
