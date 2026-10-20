package install

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFirstInstallPlansOneInitStepAndUsesTheSecret(t *testing.T) {
	state := State{Path: filepath.Join(t.TempDir(), "install.json")}
	command := Command{
		Action:       ActionInstall,
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      t.TempDir(),
		InstanceName: "demo",
		Version:      "v1.2.3",
		Secret:       strings.Repeat("s", 64),
		ServerPort:   8080,
	}

	steps, err := Next(state, command)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	initSteps := 0
	for _, step := range steps {
		if step.Init {
			initSteps++
		}
	}
	if initSteps != 1 {
		t.Errorf("first install has %d init steps, want exactly 1", initSteps)
	}

	final := StateFromSteps(steps)
	if final.JWTSecret != command.Secret {
		t.Errorf("planned secret = %q, want the generated one", final.JWTSecret)
	}
	if final.ImageTag != command.Version {
		t.Errorf("image tag = %q, want %q", final.ImageTag, command.Version)
	}
	if compose := ComposeFromSteps(steps); !strings.Contains(compose, "tasktracker-server:"+command.Version) {
		t.Errorf("compose does not pin the build version:\n%s", compose)
	}
}

func TestFirstInstallPlansAdminBootstrapAfterReady(t *testing.T) {
	state := State{Path: filepath.Join(t.TempDir(), "install.json")}
	command := Command{
		Action:           ActionInstall,
		Mode:             ModeSQLite,
		Provider:         ProviderLAN,
		DataDir:          t.TempDir(),
		InstanceName:     "demo",
		Version:          "v1.2.3",
		Secret:           strings.Repeat("s", 64),
		ServerPort:       8080,
		AdminEmail:       "admin@example.com",
		AdminPassword:    "s3cret-password",
		AdminDisplayName: "Admin",
	}

	steps, err := Next(state, command)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	adminIndex := -1
	for i, step := range steps {
		if step.Kind == StepCreateAdmin {
			adminIndex = i
			if step.Init {
				t.Error("admin bootstrap marked as an init step")
			}
			if step.Admin == nil {
				t.Fatal("admin step has no admin details")
			}
			if step.Admin.Email != command.AdminEmail || step.Admin.Password != command.AdminPassword || step.Admin.DisplayName != command.AdminDisplayName {
				t.Errorf("admin step = %+v, want the command's credentials", step.Admin)
			}
			if step.State.InstanceName != command.InstanceName {
				t.Errorf("admin step state instance = %q, want %q", step.State.InstanceName, command.InstanceName)
			}
		}
	}
	if adminIndex == -1 {
		t.Fatal("first install did not plan an admin bootstrap")
	}
	if adminIndex == 0 || steps[adminIndex-1].Kind != StepWaitReady {
		t.Errorf("admin step index = %d, want it immediately after wait-ready", adminIndex)
	}
}

func TestInstallRejectsHalfAdminCredentials(t *testing.T) {
	state := State{Path: filepath.Join(t.TempDir(), "install.json")}
	command := Command{
		Action:       ActionInstall,
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      t.TempDir(),
		InstanceName: "demo",
		Version:      "v1.2.3",
		Secret:       strings.Repeat("s", 64),
		ServerPort:   8080,
		AdminEmail:   "admin@example.com",
	}

	if _, err := Next(state, command); err == nil {
		t.Fatal("Next accepted an admin email without a password")
	}
}

// The second install is a repair. It must not create a secret, and it must
// not silently pull a newer server than the state was installed with.
func TestSecondInstallIsRepairAndPreservesTheSecret(t *testing.T) {
	state := testState(t)
	state.ImageTag = "v1.2.3"
	originalSecret := state.JWTSecret

	steps, err := Next(state, Command{
		Action:       ActionInstall,
		Mode:         state.Mode,
		Provider:     state.Provider,
		DataDir:      state.DataDir,
		InstanceName: state.InstanceName,
		Version:      "v9.9.9",
		Secret:       strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("Next repair: %v", err)
	}

	for _, step := range steps {
		if step.Init {
			t.Fatal("repair planned an init step")
		}
	}

	final := StateFromSteps(steps)
	if final.JWTSecret != originalSecret {
		t.Errorf("repair changed the JWT secret from %q to %q", originalSecret, final.JWTSecret)
	}
	if final.ImageTag != state.ImageTag {
		t.Errorf("repair changed the image tag from %q to %q", state.ImageTag, final.ImageTag)
	}
	if compose := ComposeFromSteps(steps); strings.Contains(compose, "v9.9.9") {
		t.Error("repair rendered the newer CLI version instead of the installed one")
	}
}

func TestRepairNeverPlansAdminBootstrap(t *testing.T) {
	state := testState(t)

	steps, err := Next(state, Command{
		Action:           ActionInstall,
		Mode:             state.Mode,
		Provider:         state.Provider,
		DataDir:          state.DataDir,
		InstanceName:     state.InstanceName,
		Version:          state.ImageTag,
		AdminEmail:       "someone@example.com",
		AdminPassword:    "s3cret-password",
		AdminDisplayName: "Someone",
	})
	if err != nil {
		t.Fatalf("Next repair: %v", err)
	}

	for _, step := range steps {
		if step.Kind == StepCreateAdmin {
			t.Fatal("repair planned an admin bootstrap")
		}
	}
}

func TestRepairRejectsAChangedMode(t *testing.T) {
	state := testState(t)

	_, err := Next(state, Command{
		Action:       ActionInstall,
		Mode:         ModePostgres,
		Provider:     state.Provider,
		DataDir:      state.DataDir,
		InstanceName: state.InstanceName,
		Version:      state.ImageTag,
	})
	if err == nil {
		t.Fatal("Next accepted a mode change on an existing instance")
	}
}

func TestRepairRejectsAChangedPort(t *testing.T) {
	state := testState(t)

	_, err := Next(state, Command{
		Action:       ActionInstall,
		Mode:         state.Mode,
		Provider:     state.Provider,
		DataDir:      state.DataDir,
		InstanceName: state.InstanceName,
		Version:      state.ImageTag,
		ServerPort:   9090,
	})
	if err == nil {
		t.Fatal("Next accepted a port change on an existing instance")
	}
}

func TestInstallWithoutASecretFails(t *testing.T) {
	_, err := Next(State{Path: filepath.Join(t.TempDir(), "install.json")}, Command{
		Action:       ActionInstall,
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      t.TempDir(),
		InstanceName: "demo",
		Version:      "v1.2.3",
	})
	if err == nil {
		t.Fatal("Next initialized an install with no secret")
	}
}

func TestRenderComposePinsTheBuildVersion(t *testing.T) {
	state := testState(t)
	state.ImageTag = "v9.9.9"

	compose, err := RenderCompose(state)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	if !strings.Contains(compose, "tasktracker-server:v9.9.9") {
		t.Errorf("compose does not contain the build tag:\n%s", compose)
	}
}

func TestRenderComposeSelectsTheDatabase(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		state := testState(t)
		compose, err := RenderCompose(state)
		if err != nil {
			t.Fatalf("RenderCompose: %v", err)
		}
		if !strings.Contains(compose, "sqlite:///data/tasktracker.db") {
			t.Errorf("sqlite compose has no sqlite URL:\n%s", compose)
		}
		if strings.Contains(compose, "postgres:16-alpine") {
			t.Error("sqlite compose started a postgres service")
		}
	})

	t.Run("postgres", func(t *testing.T) {
		state := testState(t)
		state.Mode = ModePostgres
		state.DatabasePassword = "p@ss:word"

		compose, err := RenderCompose(state)
		if err != nil {
			t.Fatalf("RenderCompose: %v", err)
		}
		if !strings.Contains(compose, "postgres://tasktracker:p%40ss%3Aword@postgres:5432/tasktracker") {
			t.Errorf("postgres compose does not escape the password in the URL:\n%s", compose)
		}
		if !strings.Contains(compose, "postgres:16-alpine") {
			t.Error("postgres compose has no postgres service")
		}
		if strings.Contains(compose, "sqlite:///") {
			t.Error("postgres compose still carries a sqlite URL")
		}
	})
}

func TestNextIsPure(t *testing.T) {
	state := testState(t)
	command := Command{
		Action:       ActionInstall,
		Mode:         state.Mode,
		Provider:     state.Provider,
		DataDir:      state.DataDir,
		InstanceName: state.InstanceName,
		Version:      state.ImageTag,
	}

	first, err := Next(state, command)
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	second, err := Next(state, command)
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Error("Next returned a different plan for the same input")
	}
}

func TestUninstallRequiresTheInstanceName(t *testing.T) {
	state := testState(t)

	if _, err := Next(state, Command{Action: ActionUninstall, ConfirmName: "wrong"}); err == nil {
		t.Fatal("Next allowed an uninstall without the instance name")
	}

	steps, err := Next(state, Command{Action: ActionUninstall, ConfirmName: state.InstanceName})
	if err != nil {
		t.Fatalf("Next uninstall: %v", err)
	}
	kinds := map[StepKind]bool{}
	for _, step := range steps {
		kinds[step.Kind] = true
		if step.Init {
			t.Error("uninstall planned an init step")
		}
	}
	if !kinds[StepComposeDown] || !kinds[StepRemoveState] {
		t.Errorf("uninstall steps = %v, want compose down and state removal", kinds)
	}
}

func TestCommandsOtherThanInstallRequireAnInstance(t *testing.T) {
	for _, action := range []Action{ActionLogs, ActionUninstall} {
		if _, err := Next(State{}, Command{Action: action}); err == nil {
			t.Errorf("%s succeeded with no state", action)
		}
	}
}

// The systemd unit is embedded for the CLI and kept in deploy/ for the
// self-hosting guide. Two copies are fine only while they are identical.
func TestEmbeddedSystemdUnitMatchesDeploy(t *testing.T) {
	deployed, err := os.ReadFile("../../deploy/tasktracker.service")
	if err != nil {
		t.Fatalf("read deploy unit: %v", err)
	}
	embedded, err := assets.ReadFile("embed/tasktracker.service")
	if err != nil {
		t.Fatalf("read embedded unit: %v", err)
	}
	if !bytes.Equal(deployed, embedded) {
		t.Error("deploy/tasktracker.service and internal/install/embed/tasktracker.service have drifted")
	}
}
