package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeTestState(t *testing.T) State {
	t.Helper()

	dir := t.TempDir()
	return State{
		Version:      StateVersion,
		Runtime:      RuntimeNative,
		InstanceName: "demo",
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      filepath.Join(dir, "data"),
		BinaryPath:   "/opt/tasktracker/server",
		EnvPath:      "/etc/tasktracker/secrets.env",
		UnitPath:     "/etc/systemd/system/demo.service",
		ServerBind:   NativeLoopback,
		ImageTag:     "v1.2.3",
		ServerPort:   8080,
		JWTSecret:    strings.Repeat("a", 64),
		Path:         filepath.Join(dir, "install.json"),
	}
}

func TestRenderNativeEnv(t *testing.T) {
	state := nativeTestState(t)

	env := RenderNativeEnv(state)
	for _, want := range []string{
		"JWT_SECRET=" + state.JWTSecret,
		"DATABASE_URL=sqlite://" + state.DatabaseFile(),
		"ATTACHMENT_ROOT=" + filepath.Join(state.DataDir, "blobstore"),
		"SERVER_BIND=" + NativeLoopback,
		"SERVER_PORT=8080",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("env is missing %q:\n%s", want, env)
		}
	}

	exposed := state
	exposed.ServerBind = NativeAllInterfaces
	if !strings.Contains(RenderNativeEnv(exposed), "SERVER_BIND=0.0.0.0") {
		t.Errorf("exposed env does not carry the LAN bind:\n%s", RenderNativeEnv(exposed))
	}
}

func TestRenderNativeUnit(t *testing.T) {
	state := nativeTestState(t)

	unit := RenderNativeUnit(state)
	for _, want := range []string{
		"ExecStart=" + state.BinaryPath + " serve",
		"EnvironmentFile=" + state.EnvPath,
		"User=" + NativeServiceUser,
		"ReadWritePaths=" + state.DataDir,
		"WorkingDirectory=" + state.DataDir,
		"ProtectSystem=strict",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q:\n%s", want, unit)
		}
	}
	if !strings.Contains(unit, "demo") {
		t.Errorf("unit does not name the instance:\n%s", unit)
	}
}

func TestNativeStateValidation(t *testing.T) {
	t.Run("accepts a complete native state", func(t *testing.T) {
		if err := nativeTestState(t).Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("reads a state without a runtime as docker", func(t *testing.T) {
		state := testState(t)
		if state.Runtime != "" {
			t.Fatalf("testState sets runtime %q, want the pre-native default", state.Runtime)
		}
		if err := state.Validate(); err != nil {
			t.Fatalf("a docker state with no runtime field must stay valid: %v", err)
		}
		if state.runtimeOrDefault() != RuntimeDocker {
			t.Errorf("runtimeOrDefault = %q, want docker", state.runtimeOrDefault())
		}
	})

	t.Run("refuses postgres", func(t *testing.T) {
		state := nativeTestState(t)
		state.Mode = ModePostgres
		state.DatabasePassword = "secret"
		err := state.Validate()
		if err == nil || !strings.Contains(err.Error(), "SQLite only") {
			t.Fatalf("Validate = %v, want the SQLite-only error", err)
		}
	})

	t.Run("refuses a non-lan provider", func(t *testing.T) {
		state := nativeTestState(t)
		state.Provider = ProviderTailscale
		err := state.Validate()
		if err == nil || !strings.Contains(err.Error(), "lan only") {
			t.Fatalf("Validate = %v, want the lan-only error", err)
		}
	})

	t.Run("refuses a relative binary", func(t *testing.T) {
		state := nativeTestState(t)
		state.BinaryPath = "server"
		err := state.Validate()
		if err == nil || !strings.Contains(err.Error(), "not absolute") {
			t.Fatalf("Validate = %v, want the absolute-path error", err)
		}
	})

	t.Run("refuses whitespace in a unit path", func(t *testing.T) {
		state := nativeTestState(t)
		state.DataDir = "/var/lib/task tracker"
		err := state.Validate()
		if err == nil || !strings.Contains(err.Error(), "whitespace") {
			t.Fatalf("Validate = %v, want the whitespace error", err)
		}
	})
}

func TestNativeInstallPlan(t *testing.T) {
	dir := t.TempDir()
	cmd := Command{
		Action:           ActionInstall,
		Runtime:          RuntimeNative,
		Mode:             ModeSQLite,
		Provider:         ProviderLAN,
		DataDir:          filepath.Join(dir, "data"),
		InstanceName:     "tasktracker",
		Version:          "v1.2.3",
		Secret:           strings.Repeat("b", 64),
		ServerPort:       8080,
		BinaryPath:       "/opt/tasktracker/server",
		AdminEmail:       "admin@example.com",
		AdminPassword:    "password123",
		AdminDisplayName: "Admin",
	}

	steps, err := Next(State{Path: filepath.Join(dir, "install.json")}, cmd)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	want := []StepKind{
		StepWriteState, StepEnsureUser, StepWriteEnv, StepWriteUnit,
		StepStartService, StepWaitReady, StepCreateAdmin,
	}
	if len(steps) != len(want) {
		t.Fatalf("steps = %d, want %d: %+v", len(steps), len(want), steps)
	}
	for i, kind := range want {
		if steps[i].Kind != kind {
			t.Errorf("step %d = %q, want %q", i, steps[i].Kind, kind)
		}
	}

	env := steps[2].Env
	if !strings.Contains(env, "DATABASE_URL=sqlite://"+filepath.Join(cmd.DataDir, "tasktracker.db")) {
		t.Errorf("env does not point at the instance database:\n%s", env)
	}
	if !strings.Contains(steps[3].Unit, "ExecStart=/opt/tasktracker/server serve") {
		t.Errorf("unit does not start the binary:\n%s", steps[3].Unit)
	}
}

func TestNativeRepairPlanSkipsTheAdmin(t *testing.T) {
	state := nativeTestState(t)

	steps, err := Next(state, Command{
		Action:       ActionInstall,
		Runtime:      RuntimeNative,
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      state.DataDir,
		InstanceName: state.InstanceName,
		Version:      state.ImageTag,
		ServerPort:   state.ServerPort,
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if steps[0].Kind != StepWriteState || steps[0].Init {
		t.Errorf("repair starts with %q init=%v, want a non-init state write", steps[0].Kind, steps[0].Init)
	}
	for _, step := range steps {
		if step.Kind == StepCreateAdmin {
			t.Fatal("a repair must not create a second admin")
		}
	}
}

func TestNativeLogsAndUninstallPlans(t *testing.T) {
	state := nativeTestState(t)

	logs, err := Next(state, Command{Action: ActionLogs})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(logs) != 1 || logs[0].Kind != StepServiceLogs {
		t.Errorf("logs = %+v, want one %q step", logs, StepServiceLogs)
	}

	uninstall, err := Next(state, Command{Action: ActionUninstall, ConfirmName: state.InstanceName})
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	want := []StepKind{StepStopService, StepRemoveEnv, StepRemoveState}
	if len(uninstall) != len(want) {
		t.Fatalf("uninstall = %d steps, want %d", len(uninstall), len(want))
	}
	for i, kind := range want {
		if uninstall[i].Kind != kind {
			t.Errorf("uninstall step %d = %q, want %q", i, uninstall[i].Kind, kind)
		}
	}
}

func TestInstallCannotChangeTheRuntimeInPlace(t *testing.T) {
	state := nativeTestState(t)

	_, err := Next(state, Command{
		Action:       ActionInstall,
		Runtime:      RuntimeDocker,
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      state.DataDir,
		InstanceName: state.InstanceName,
		Version:      state.ImageTag,
		ServerPort:   state.ServerPort,
	})
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("Next = %v, want a refusal to change runtime in place", err)
	}
}

func nativePreflightOptions() PreflightOptions {
	return PreflightOptions{
		Runtime:    RuntimeNative,
		DataDir:    "/var/lib/tasktracker",
		Mode:       ModeSQLite,
		Port:       8080,
		Platform:   "linux",
		BinaryPath: "/opt/tasktracker/server",
	}
}

func TestNativePreflightNeverConsultsDocker(t *testing.T) {
	env := &fakePreflightEnv{statMode: 0o755}

	if err := preflight(context.Background(), env, nativePreflightOptions()); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	for _, name := range env.lookPathCalls {
		if name == "docker" {
			t.Fatalf("native preflight looked up docker: %v", env.lookPathCalls)
		}
	}
	if len(env.ensurePaths) != 1 || env.ensurePaths[0] != "/var/lib/tasktracker" {
		t.Errorf("writability checks = %v, want the data directory", env.ensurePaths)
	}
}

func TestNativePreflightRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*fakePreflightEnv, *PreflightOptions)
		wantErr string
	}{
		{
			name:    "not root",
			mutate:  func(env *fakePreflightEnv, _ *PreflightOptions) { env.euid = 1000 },
			wantErr: "needs root",
		},
		{
			name:    "binary missing",
			mutate:  func(env *fakePreflightEnv, _ *PreflightOptions) { env.statErr = os.ErrNotExist },
			wantErr: "not readable",
		},
		{
			name:    "binary is a directory",
			mutate:  func(env *fakePreflightEnv, _ *PreflightOptions) { env.statIsDir = true },
			wantErr: "is a directory",
		},
		{
			name:    "binary not executable",
			mutate:  func(env *fakePreflightEnv, _ *PreflightOptions) { env.statMode = 0o644 },
			wantErr: "not executable",
		},
		{
			name:    "postgres",
			mutate:  func(_ *fakePreflightEnv, opts *PreflightOptions) { opts.Mode = ModePostgres },
			wantErr: "SQLite only",
		},
		{
			name:    "no binary path",
			mutate:  func(_ *fakePreflightEnv, opts *PreflightOptions) { opts.BinaryPath = "" },
			wantErr: "--server-binary",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := nativePreflightOptions()
			env := &fakePreflightEnv{statMode: 0o755}
			test.mutate(env, &opts)

			err := preflight(context.Background(), env, opts)
			if err == nil {
				t.Fatalf("preflight accepted %s", test.name)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("error = %q, want it to name %q", err, test.wantErr)
			}
		})
	}
}

func TestNativeChecksDoNotReadCompose(t *testing.T) {
	state := nativeTestState(t)
	env := &fakeCheckEnv{
		get: func(string) (httpResult, error) {
			return httpResult{StatusCode: 200, Date: fixedNow}, nil
		},
	}

	checks := runChecks(context.Background(), env, state, false)
	if len(env.composeCalls) != 0 {
		t.Errorf("native checks called compose: %v", env.composeCalls)
	}
	if len(env.serviceCalls) != 1 || env.serviceCalls[0] != state.ServiceName() {
		t.Errorf("service calls = %v, want the instance unit", env.serviceCalls)
	}
	if len(env.statCalls) != 1 || env.statCalls[0] != state.DatabaseFile() {
		t.Errorf("stat calls = %v, want the SQLite file", env.statCalls)
	}

	for name, want := range map[string]CheckStatus{
		"service":  CheckOK,
		"readyz":   CheckOK,
		"address":  CheckOK,
		"database": CheckOK,
		"backup":   CheckSkip,
	} {
		if got := checkByName(checks, name); got.Status != want {
			t.Errorf("%s = %q (%s), want %q", name, got.Status, got.Summary, want)
		}
	}
}

func TestNativeChecksReportAStoppedServiceAndABareBind(t *testing.T) {
	state := nativeTestState(t)
	env := &fakeCheckEnv{
		service: func() (string, error) { return "failed", nil },
		stat: func(string) (os.FileInfo, error) {
			return fakeFileInfo{size: 4096}, nil
		},
	}

	failed := checkService(context.Background(), env, state)
	if failed.Status != CheckFail || !strings.Contains(failed.Summary, "failed") {
		t.Errorf("service check = %+v, want a failure naming the unit state", failed)
	}
	if failed.Fix == "" {
		t.Error("a failed service check must name the command that shows why")
	}

	state.ServerBind = "192.168.1.42"
	if got := checkNativeAddress(state); got.Status != CheckFail {
		t.Errorf("address check = %+v, want a failure for a bind tt never renders", got)
	}
	if got := checkNativeDatabase(env, state); got.Status != CheckOK {
		t.Errorf("database check = %+v, want ok", got)
	}
}
