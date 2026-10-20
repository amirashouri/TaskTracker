package install

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedNow is the fake clock every check test compares against.
var fixedNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

type fakeCheckEnv struct {
	compose      func(args []string) (string, error)
	get          func(address string) (httpResult, error)
	now          time.Time
	composeCalls []string
	getCalls     []string
}

func (e *fakeCheckEnv) Compose(_ context.Context, _ State, args ...string) (string, error) {
	e.composeCalls = append(e.composeCalls, strings.Join(args, " "))
	if e.compose == nil {
		return "", nil
	}
	return e.compose(args)
}

func (e *fakeCheckEnv) Get(_ context.Context, address string) (httpResult, error) {
	e.getCalls = append(e.getCalls, address)
	if e.get == nil {
		return httpResult{}, nil
	}
	return e.get(address)
}

func (e *fakeCheckEnv) Now() time.Time {
	if e.now.IsZero() {
		return fixedNow
	}
	return e.now
}

// healthyCompose is the normal output of a running SQLite-style install.
func healthyCompose(args []string) (string, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "ps"):
		return `[{"Service":"server","State":"running","Health":"healthy"}]`, nil
	case strings.HasPrefix(joined, "port"):
		return "127.0.0.1:8080", nil
	case strings.Contains(joined, "stat"):
		return "1048576", nil
	default:
		return "", nil
	}
}

func checkByName(checks []Check, name string) Check {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return Check{}
}

func TestParseComposePSAcceptsBothShapes(t *testing.T) {
	array := `[{"Service":"server","State":"running"},{"Service":"postgres","State":"running"}]`
	services, err := parseComposePS(array)
	if err != nil {
		t.Fatalf("parse JSON array: %v", err)
	}
	if len(services) != 2 || services[1].Service != "postgres" {
		t.Errorf("array services = %+v, want server and postgres", services)
	}

	lines := "{\"Service\":\"server\",\"State\":\"running\"}\n\n{\"Service\":\"postgres\",\"State\":\"running\"}\n"
	services, err = parseComposePS(lines)
	if err != nil {
		t.Fatalf("parse JSON lines: %v", err)
	}
	if len(services) != 2 || services[0].Service != "server" {
		t.Errorf("line services = %+v, want server and postgres", services)
	}

	if services, err := parseComposePS("  \n"); err != nil || len(services) != 0 {
		t.Errorf("empty output = %+v, %v; want no services and no error", services, err)
	}
	if _, err := parseComposePS("{not json"); err == nil {
		t.Error("parseComposePS accepted malformed JSON")
	}
}

func TestContainerCheck(t *testing.T) {
	tests := []struct {
		name    string
		mode    Mode
		output  string
		err     error
		want    CheckStatus
		summary string
	}{
		{
			name:    "running",
			output:  `[{"Service":"server","State":"running","Health":"healthy"}]`,
			want:    CheckOK,
			summary: "server running",
		},
		{
			name:    "postgres both running",
			mode:    ModePostgres,
			output:  `[{"Service":"server","State":"running","Health":"healthy"},{"Service":"postgres","State":"running","Health":"healthy"}]`,
			want:    CheckOK,
			summary: "server and postgres running",
		},
		{
			name:    "json lines",
			output:  "{\"Service\":\"server\",\"State\":\"running\"}\n",
			want:    CheckOK,
			summary: "server running",
		},
		{
			name:    "missing server",
			output:  `[]`,
			want:    CheckFail,
			summary: "server container is missing",
		},
		{
			name:    "exited",
			output:  `[{"Service":"server","State":"exited","Status":"Exited (1)"}]`,
			want:    CheckFail,
			summary: "server is exited",
		},
		{
			name:    "unhealthy",
			output:  `[{"Service":"server","State":"running","Health":"unhealthy"}]`,
			want:    CheckFail,
			summary: "server is unhealthy",
		},
		{
			name:    "starting",
			output:  `[{"Service":"server","State":"running","Health":"starting"}]`,
			want:    CheckWarn,
			summary: "healthcheck is still starting",
		},
		{
			name:    "postgres missing",
			mode:    ModePostgres,
			output:  `[{"Service":"server","State":"running","Health":"healthy"}]`,
			want:    CheckFail,
			summary: "postgres container is missing",
		},
		{
			name:    "compose error",
			err:     errors.New("daemon not running"),
			want:    CheckFail,
			summary: "cannot read the container state",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := testState(t)
			state.Mode = test.mode
			env := &fakeCheckEnv{compose: func([]string) (string, error) {
				return test.output, test.err
			}}

			check := checkContainers(context.Background(), env, state)
			if check.Status != test.want {
				t.Errorf("status = %q, want %q (%s)", check.Status, test.want, check.Summary)
			}
			if !strings.Contains(check.Summary, test.summary) {
				t.Errorf("summary = %q, want it to contain %q", check.Summary, test.summary)
			}
			if test.want == CheckFail && check.Fix == "" {
				t.Error("a failed check has no repair instruction")
			}
		})
	}
}

func TestReadyCheck(t *testing.T) {
	tests := []struct {
		name    string
		result  httpResult
		err     error
		want    CheckStatus
		summary string
	}{
		{"ready", httpResult{StatusCode: 200}, nil, CheckOK, "answered 200"},
		{"not ready", httpResult{StatusCode: 503}, nil, CheckFail, "HTTP 503"},
		{"no answer", httpResult{}, errors.New("connection refused"), CheckFail, "no response"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := &fakeCheckEnv{get: func(string) (httpResult, error) {
				return test.result, test.err
			}}

			check := checkReady(context.Background(), env, testState(t))
			if check.Status != test.want {
				t.Errorf("status = %q, want %q", check.Status, test.want)
			}
			if !strings.Contains(check.Summary, test.summary) {
				t.Errorf("summary = %q, want it to contain %q", check.Summary, test.summary)
			}
			wantAddress := "http://127.0.0.1:8080/readyz"
			if len(env.getCalls) != 1 || env.getCalls[0] != wantAddress {
				t.Errorf("GET calls = %v, want %s", env.getCalls, wantAddress)
			}
		})
	}
}

func TestAddressCheck(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		err     error
		want    CheckStatus
		summary string
	}{
		{"loopback", "127.0.0.1:8080\n", nil, CheckOK, "published at 127.0.0.1:8080"},
		{"all interfaces", "0.0.0.0:8080\n", nil, CheckFail, "expected 127.0.0.1:8080"},
		{"ipv6 wildcard", "[::]:8080\n", nil, CheckFail, "expected 127.0.0.1:8080"},
		{"no publish", "", nil, CheckFail, "publishes no host port"},
		{"compose error", "", errors.New("no such service"), CheckFail, "cannot read the published address"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := &fakeCheckEnv{compose: func([]string) (string, error) {
				return test.output, test.err
			}}

			check := checkAddress(context.Background(), env, testState(t))
			if check.Status != test.want {
				t.Errorf("status = %q, want %q", check.Status, test.want)
			}
			if !strings.Contains(check.Summary, test.summary) {
				t.Errorf("summary = %q, want it to contain %q", check.Summary, test.summary)
			}
		})
	}
}

func TestDatabaseCheck(t *testing.T) {
	tests := []struct {
		name    string
		mode    Mode
		output  string
		err     error
		want    CheckStatus
		summary string
		command string
	}{
		{"sqlite", ModeSQLite, "1048576", nil, CheckOK, "1.0 MiB", "stat"},
		{"postgres", ModePostgres, "2048\n", nil, CheckOK, "2.0 KiB", "pg_database_size"},
		{"unparseable", ModeSQLite, "not a size", nil, CheckFail, "returned", "stat"},
		{"command error", ModeSQLite, "", errors.New("container not running"), CheckFail, "container not running", "stat"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := testState(t)
			state.Mode = test.mode
			var called string
			env := &fakeCheckEnv{compose: func(args []string) (string, error) {
				called = strings.Join(args, " ")
				return test.output, test.err
			}}

			check := checkDatabase(context.Background(), env, state)
			if check.Status != test.want {
				t.Errorf("status = %q, want %q", check.Status, test.want)
			}
			detail := check.Summary + " " + check.Fix
			if !strings.Contains(detail, test.summary) {
				t.Errorf("summary and fix = %q, want them to contain %q", detail, test.summary)
			}
			if !strings.Contains(called, test.command) {
				t.Errorf("compose args = %q, want it to contain %q", called, test.command)
			}
		})
	}
}

func TestBackupCheckIsASkipWithTheManualProcedure(t *testing.T) {
	check := checkBackup()
	if check.Status != CheckSkip {
		t.Errorf("status = %q, want skip", check.Status)
	}
	if !strings.Contains(check.Fix, "SELF-HOSTING") {
		t.Errorf("fix = %q, want a pointer to the self-hosting guide", check.Fix)
	}
}

func TestClockCheck(t *testing.T) {
	tests := []struct {
		name    string
		date    time.Time
		err     error
		want    CheckStatus
		summary string
	}{
		{"agree", fixedNow.Add(-30 * time.Second), nil, CheckOK, "agree"},
		{"warn", fixedNow.Add(-3 * time.Minute), nil, CheckWarn, "3m0s"},
		{"fail", fixedNow.Add(-10 * time.Minute), nil, CheckFail, "10m0s"},
		{"no header", time.Time{}, nil, CheckSkip, "no Date header"},
		{"no answer", time.Time{}, errors.New("connection refused"), CheckFail, "did not answer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := &fakeCheckEnv{
				now: fixedNow,
				get: func(string) (httpResult, error) {
					return httpResult{StatusCode: 200, Date: test.date}, test.err
				},
			}

			check := checkClock(context.Background(), env, testState(t))
			if check.Status != test.want {
				t.Errorf("status = %q, want %q", check.Status, test.want)
			}
			if !strings.Contains(check.Summary, test.summary) {
				t.Errorf("summary = %q, want it to contain %q", check.Summary, test.summary)
			}
		})
	}
}

func TestProviderCheckIsNeverGreen(t *testing.T) {
	state := testState(t)
	check := checkProvider(state)
	if check.Status != CheckSkip {
		t.Errorf("status = %q, want skip until tt expose lands", check.Status)
	}
	if !strings.Contains(check.Summary, string(state.Provider)) {
		t.Errorf("summary = %q, want the provider name", check.Summary)
	}
	if !strings.Contains(check.Fix, "tt expose") {
		t.Errorf("fix = %q, want a pointer to tt expose", check.Fix)
	}
}

func TestStatusChecksAreInAFixedOrder(t *testing.T) {
	env := &fakeCheckEnv{
		compose: healthyCompose,
		get: func(string) (httpResult, error) {
			return httpResult{StatusCode: 200, Date: fixedNow}, nil
		},
		now: fixedNow,
	}

	want := []string{"containers", "readyz", "address", "database", "backup"}
	checks := runChecks(context.Background(), env, testState(t), false)
	if len(checks) != len(want) {
		t.Fatalf("status checks = %d, want %d", len(checks), len(want))
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("check %d = %q, want %q", i, checks[i].Name, name)
		}
	}
}

// The two commands must never disagree. Doctor is status plus two checks, and
// the shared prefix has to be identical, not merely green in both.
func TestDoctorIsStatusPlusDoctorChecks(t *testing.T) {
	env := &fakeCheckEnv{
		compose: healthyCompose,
		get: func(string) (httpResult, error) {
			return httpResult{StatusCode: 200, Date: fixedNow}, nil
		},
		now: fixedNow,
	}
	state := testState(t)

	statusChecks := runChecks(context.Background(), env, state, false)
	doctorChecks := runChecks(context.Background(), env, state, true)

	if len(doctorChecks) != len(statusChecks)+2 {
		t.Fatalf("doctor checks = %d, want status %d plus 2", len(doctorChecks), len(statusChecks))
	}
	for i, check := range statusChecks {
		if doctorChecks[i] != check {
			t.Errorf("check %q differs between status (%+v) and doctor (%+v)", check.Name, check, doctorChecks[i])
		}
	}
	if doctorChecks[len(statusChecks)].Name != "clock" {
		t.Errorf("first doctor-only check = %q, want clock", doctorChecks[len(statusChecks)].Name)
	}
	if doctorChecks[len(statusChecks)+1].Name != "provider" {
		t.Errorf("last doctor-only check = %q, want provider", doctorChecks[len(statusChecks)+1].Name)
	}
}

func TestChecksNeverPrintInstallSecrets(t *testing.T) {
	state := testState(t)
	state.Mode = ModePostgres
	state.DatabasePassword = "database-secret-value"

	env := &fakeCheckEnv{
		compose: func(args []string) (string, error) {
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "ps"):
				return `[{"Service":"server","State":"running","Health":"healthy"},{"Service":"postgres","State":"running","Health":"healthy"}]`, nil
			case strings.HasPrefix(joined, "port"):
				return "127.0.0.1:8080", nil
			case strings.Contains(joined, "psql"):
				return "4096", nil
			default:
				return "", nil
			}
		},
		get: func(string) (httpResult, error) {
			return httpResult{StatusCode: 200, Date: fixedNow}, nil
		},
		now: fixedNow,
	}

	for _, check := range runChecks(context.Background(), env, state, true) {
		for _, text := range []string{check.Summary, check.Fix} {
			if strings.Contains(text, state.JWTSecret) || strings.Contains(text, state.DatabasePassword) {
				t.Errorf("check %q printed an install secret: %q", check.Name, text)
			}
		}
	}
}
