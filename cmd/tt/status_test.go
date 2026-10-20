package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amirashouri/tasktracker-server/internal/install"
)

func runChecksCommand(
	t *testing.T,
	command string,
	args []string,
	statePath string,
	doctor bool,
	checks install.CheckFunc,
) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := runCheckCommand(context.Background(), command, args, statusDeps{
		stdout:    &stdout,
		stderr:    &stderr,
		statePath: statePath,
		checks:    checks,
	}, doctor)
	return code, stdout.String(), stderr.String()
}

func TestStatusPrintsEveryCheckAndSucceeds(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)

	var doctorArg bool
	checks := func(_ context.Context, _ install.State, doctor bool) []install.Check {
		doctorArg = doctor
		return []install.Check{
			{Name: "containers", Status: install.CheckOK, Summary: "server running"},
			{Name: "readyz", Status: install.CheckWarn, Summary: "healthcheck is still starting"},
			{Name: "backup", Status: install.CheckSkip, Summary: "no backup recorded", Fix: "follow the guide"},
		}
	}

	code, stdout, stderr := runChecksCommand(t, "status", nil, statePath, false, checks)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if doctorArg {
		t.Error("status ran the doctor checks")
	}
	for _, want := range []string{"[ok]", "[warn]", "[skip]", "containers", "follow the guide", "instance: demo"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not contain %q:\n%s", want, stdout)
		}
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty on success", stderr)
	}
}

func TestStatusFailsWhenACheckFails(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	checks := func(context.Context, install.State, bool) []install.Check {
		return []install.Check{
			{Name: "containers", Status: install.CheckFail, Summary: "server is missing"},
			{Name: "readyz", Status: install.CheckFail, Summary: "no response"},
			{Name: "backup", Status: install.CheckSkip, Summary: "no backup recorded"},
		}
	}

	code, stdout, stderr := runChecksCommand(t, "status", nil, statePath, false, checks)

	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stdout, "[fail]") {
		t.Errorf("stdout does not show a failed check:\n%s", stdout)
	}
	if !strings.Contains(stderr, "2 checks failed") {
		t.Errorf("stderr = %q, want the failure count", stderr)
	}
}

func TestWarningsAndSkipsDoNotFailTheCommand(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)
	checks := func(context.Context, install.State, bool) []install.Check {
		return []install.Check{
			{Name: "containers", Status: install.CheckWarn, Summary: "still starting"},
			{Name: "provider", Status: install.CheckSkip, Summary: "not checked yet"},
		}
	}

	code, _, stderr := runChecksCommand(t, "doctor", nil, statePath, true, checks)

	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}

func TestDoctorRunsTheDoctorChecks(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	seedState(t, statePath)

	var seen []bool
	checks := func(_ context.Context, _ install.State, doctor bool) []install.Check {
		seen = append(seen, doctor)
		return []install.Check{{Name: "clock", Status: install.CheckOK, Summary: "clocks agree"}}
	}

	if code, _, _ := runChecksCommand(t, "doctor", nil, statePath, true, checks); code != 0 {
		t.Fatalf("doctor exit = %d", code)
	}
	if code, _, _ := runChecksCommand(t, "status", nil, statePath, false, checks); code != 0 {
		t.Fatalf("status exit = %d", code)
	}

	want := []bool{true, false}
	if len(seen) != len(want) || seen[0] != want[0] || seen[1] != want[1] {
		t.Errorf("doctor flags = %v, want %v", seen, want)
	}
}

func TestStatusWithoutAnInstallationRefusesAndRunsNoChecks(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "install.json")
	called := false
	checks := func(context.Context, install.State, bool) []install.Check {
		called = true
		return nil
	}

	code, stdout, stderr := runChecksCommand(t, "status", nil, statePath, false, checks)

	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if called {
		t.Error("status ran checks without an installation")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing", stdout)
	}
	if !strings.Contains(stderr, "no installation found") {
		t.Errorf("stderr = %q, want an install hint", stderr)
	}
}
