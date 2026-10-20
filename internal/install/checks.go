package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// CheckStatus is one check's outcome. Skip is deliberate: the check exists but
// cannot be proved in this install (no provider, no backup), and it must never
// be counted as a pass.
type CheckStatus string

const (
	CheckOK   CheckStatus = "ok"
	CheckWarn CheckStatus = "warn"
	CheckFail CheckStatus = "fail"
	CheckSkip CheckStatus = "skip"
)

// Check is one line of `tt status` or `tt doctor` output.
type Check struct {
	Name    string
	Status  CheckStatus
	Summary string
	Fix     string
}

// CheckFunc runs the read-only checks. The CLI takes it as a dependency so its
// output and exit code are testable without a Docker daemon.
type CheckFunc func(ctx context.Context, state State, doctor bool) []Check

// Checks is the production entry point for both commands.
func Checks(ctx context.Context, state State, doctor bool) []Check {
	return runChecks(ctx, osCheckEnv{}, state, doctor)
}

// checkEnv is the host surface the checks read. It exists so every outcome is
// provable without Docker, a listening server, or a real clock.
type checkEnv interface {
	Compose(ctx context.Context, state State, args ...string) (string, error)
	Get(ctx context.Context, address string) (httpResult, error)
	Now() time.Time
}

type httpResult struct {
	StatusCode int
	Date       time.Time
}

type osCheckEnv struct{}

func (osCheckEnv) Compose(ctx context.Context, state State, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", composeArgs(state, args...)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (osCheckEnv) Get(ctx context.Context, address string) (httpResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return httpResult{}, err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return httpResult{}, err
	}
	response.Body.Close()

	// ParseTime returns the zero time when the header is absent; the clock
	// check reports that as a skip rather than inventing agreement.
	date, _ := http.ParseTime(response.Header.Get("Date"))
	return httpResult{StatusCode: response.StatusCode, Date: date}, nil
}

func (osCheckEnv) Now() time.Time {
	return time.Now()
}

// runChecks is ordered: doctor is status plus the checks that only matter when
// something is wrong. Sharing the first five functions is what keeps the two
// commands from disagreeing.
func runChecks(ctx context.Context, env checkEnv, state State, doctor bool) []Check {
	checks := []Check{
		checkContainers(ctx, env, state),
		checkReady(ctx, env, state),
		checkAddress(ctx, env, state),
		checkDatabase(ctx, env, state),
		checkBackup(),
	}
	if doctor {
		checks = append(checks, checkClock(ctx, env, state), checkProvider(state))
	}
	return checks
}

func newCheck(name string, status CheckStatus, summary, fix string) Check {
	return Check{Name: name, Status: status, Summary: summary, Fix: fix}
}

// composeService is the subset of `docker compose ps --format json` the checks
// read. Status is human text ("Up 2 minutes (healthy)"); State and Health are
// the machine-readable fields.
type composeService struct {
	Service string `json:"Service"`
	State   string `json:"State"`
	Health  string `json:"Health"`
	Status  string `json:"Status"`
}

// parseComposePS accepts both shapes Compose emits: a JSON array on newer
// releases and one JSON object per line on older ones.
func parseComposePS(output string) ([]composeService, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var services []composeService
		if err := json.Unmarshal([]byte(trimmed), &services); err != nil {
			return nil, fmt.Errorf("decode compose ps: %w", err)
		}
		return services, nil
	}

	var services []composeService
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var service composeService
		if err := json.Unmarshal([]byte(line), &service); err != nil {
			return nil, fmt.Errorf("decode compose ps line: %w", err)
		}
		services = append(services, service)
	}
	return services, nil
}

func checkContainers(ctx context.Context, env checkEnv, state State) Check {
	output, err := env.Compose(ctx, state, "ps", "--format", "json")
	if err != nil {
		return newCheck("containers", CheckFail, "cannot read the container state",
			fmt.Sprintf("%v. %s", err, dockerStartHint(runtime.GOOS)))
	}

	services, err := parseComposePS(output)
	if err != nil {
		return newCheck("containers", CheckFail, "cannot read the container state", err.Error())
	}
	byService := make(map[string]composeService, len(services))
	for _, service := range services {
		byService[service.Service] = service
	}

	required := []string{"server"}
	if state.Mode == ModePostgres {
		required = append(required, "postgres")
	}

	var problems, warnings []string
	for _, name := range required {
		service, ok := byService[name]
		if !ok {
			problems = append(problems, name+" container is missing")
			continue
		}
		if service.State != "running" {
			problems = append(problems, fmt.Sprintf("%s is %s", name, containerState(service)))
			continue
		}
		switch service.Health {
		case "unhealthy":
			problems = append(problems, name+" is unhealthy")
		case "starting":
			warnings = append(warnings, name+" healthcheck is still starting")
		}
	}

	if len(problems) > 0 {
		return newCheck("containers", CheckFail, strings.Join(problems, "; "),
			"run `tt install` to repair the containers")
	}
	if len(warnings) > 0 {
		return newCheck("containers", CheckWarn, strings.Join(warnings, "; "), "")
	}
	return newCheck("containers", CheckOK, strings.Join(required, " and ")+" running", "")
}

func containerState(service composeService) string {
	switch {
	case service.State != "":
		return service.State
	case service.Status != "":
		return service.Status
	default:
		return "unknown"
	}
}

func checkReady(ctx context.Context, env checkEnv, state State) Check {
	address := fmt.Sprintf("http://127.0.0.1:%d/readyz", state.ServerPort)
	result, err := env.Get(ctx, address)
	if err != nil {
		return newCheck("readyz", CheckFail, "no response from "+address,
			"run `tt logs` to see why the server is not serving")
	}
	if result.StatusCode != http.StatusOK {
		return newCheck("readyz", CheckFail, fmt.Sprintf("%s returned HTTP %d", address, result.StatusCode),
			"run `tt logs`; the database or a pending migration is the usual cause")
	}
	return newCheck("readyz", CheckOK, "the server answered 200", "")
}

func checkAddress(ctx context.Context, env checkEnv, state State) Check {
	published, err := env.Compose(ctx, state, "port", "server", "8080")
	if err != nil {
		return newCheck("address", CheckFail, "cannot read the published address",
			"run `tt install` to repair the container")
	}

	expected := fmt.Sprintf("127.0.0.1:%d", state.ServerPort)
	addresses := strings.Fields(published)
	for _, address := range addresses {
		if address == expected {
			return newCheck("address", CheckOK, "published at "+expected, "")
		}
	}
	if len(addresses) == 0 {
		return newCheck("address", CheckFail, "the server publishes no host port",
			"run `tt install` to restore the loopback publish")
	}
	return newCheck("address", CheckFail,
		"published at "+strings.Join(addresses, ", ")+", expected "+expected,
		"run `tt install` to restore the loopback publish")
}

func checkDatabase(ctx context.Context, env checkEnv, state State) Check {
	var args []string
	var source string
	if state.Mode == ModePostgres {
		args = []string{
			"exec", "-T", "postgres", "psql", "-U", "tasktracker", "-d", "tasktracker",
			"-tAc", "SELECT pg_database_size(current_database())",
		}
		source = "pg_database_size"
	} else {
		args = []string{"exec", "-T", "server", "stat", "-c", "%s", "/data/tasktracker.db"}
		source = "the SQLite file size"
	}

	output, err := env.Compose(ctx, state, args...)
	if err != nil {
		return newCheck("database", CheckFail, "cannot read the database size", err.Error())
	}
	size, err := strconv.ParseUint(strings.TrimSpace(output), 10, 64)
	if err != nil {
		return newCheck("database", CheckFail, "cannot read the database size",
			fmt.Sprintf("%s returned %q", source, strings.TrimSpace(output)))
	}
	return newCheck("database", CheckOK, humanBytes(size)+" on disk", "")
}

// checkBackup is a skip, not a warning: tt does not create backups, so there is
// no age it can honestly report. A permanent yellow line would train the
// operator to ignore the output.
func checkBackup() Check {
	return newCheck("backup", CheckSkip, "no backup recorded; tt does not create backups",
		"follow docs/SELF-HOSTING.md §9 and test a restore")
}

const (
	clockWarnSkew = 2 * time.Minute
	clockFailSkew = 5 * time.Minute
)

func checkClock(ctx context.Context, env checkEnv, state State) Check {
	address := fmt.Sprintf("http://127.0.0.1:%d/readyz", state.ServerPort)
	result, err := env.Get(ctx, address)
	if err != nil {
		return newCheck("clock", CheckFail, "cannot compare clocks: the server did not answer",
			"run `tt logs`; the server must answer /readyz")
	}
	if result.Date.IsZero() {
		return newCheck("clock", CheckSkip, "the server sent no Date header",
			"check the reverse proxy; it may be stripping the header")
	}

	skew := env.Now().Sub(result.Date)
	if skew < 0 {
		skew = -skew
	}
	skew = skew.Round(time.Second)
	switch {
	case skew > clockFailSkew:
		return newCheck("clock", CheckFail, fmt.Sprintf("local and server clocks differ by %s", skew),
			"fix NTP on both hosts; TLS and token lifetimes fail on large skew")
	case skew > clockWarnSkew:
		return newCheck("clock", CheckWarn, fmt.Sprintf("local and server clocks differ by %s", skew),
			"check NTP on both hosts")
	default:
		return newCheck("clock", CheckOK, "local and server clocks agree", "")
	}
}

func checkProvider(state State) Check {
	return newCheck("provider", CheckSkip,
		fmt.Sprintf("%s: provider health is not checked yet", state.Provider),
		"DNS, TLS, the external request, and the attachment cap arrive with `tt expose` (T9.8–T9.10)")
}
