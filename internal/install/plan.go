package install

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Action is the command a plan is for.
type Action string

const (
	ActionInstall            Action = "install"
	ActionLogs               Action = "logs"
	ActionUninstall          Action = "uninstall"
	ActionAdminList          Action = "admin-list"
	ActionAdminResetPassword Action = "admin-reset-password"
)

// Command is everything the plan may read besides the state. Defaults are
// resolved by the CLI, not here, so Next stays a pure function.
type Command struct {
	Action       Action
	Runtime      Runtime
	Mode         Mode
	Provider     Provider
	DataDir      string
	InstanceName string
	Version      string
	Secret       string
	DBPassword   string
	ConfirmName  string
	ServerPort   int
	// BinaryPath is the server binary a native install supervises. A Docker
	// install ignores it: the image carries the binary.
	BinaryPath string

	// Admin bootstrap is first-install only. Repair ignores these fields so a
	// re-run cannot create a second account or resurrect a deleted one.
	AdminEmail       string
	AdminPassword    string
	AdminDisplayName string
}

// AdminBootstrap is the first account created after the server is ready. It
// travels on the plan step rather than in State: the password must never reach
// install.json.
type AdminBootstrap struct {
	Email       string
	Password    string
	DisplayName string
}

// StepKind names one unit of work. The plan returns steps; the runner is the
// only code that knows how to execute one.
type StepKind string

const (
	StepWriteState   StepKind = "write-state"
	StepWriteCompose StepKind = "write-compose"
	StepComposeUp    StepKind = "compose-up"
	StepWaitReady    StepKind = "wait-ready"
	StepCreateAdmin  StepKind = "create-admin"
	StepComposeLogs  StepKind = "compose-logs"
	StepComposeDown  StepKind = "compose-down"
	StepRemoveState  StepKind = "remove-state"

	// Native runtime. The systemd unit, the env file, and the service account
	// are the deployment; there is no image to pull and no compose file.
	StepEnsureUser         StepKind = "ensure-user"
	StepWriteEnv           StepKind = "write-env"
	StepWriteUnit          StepKind = "write-unit"
	StepStartService       StepKind = "start-service"
	StepRestartService     StepKind = "restart-service"
	StepStopService        StepKind = "stop-service"
	StepRemoveEnv          StepKind = "remove-env"
	StepServiceLogs        StepKind = "service-logs"
	StepAdminList          StepKind = "admin-list"
	StepAdminResetPassword StepKind = "admin-reset-password"
)

// Step is one action in a plan. Init marks the step that creates the state and
// secret, so a repair can be asserted to contain none of them.
type Step struct {
	Kind        StepKind
	Description string
	Init        bool
	State       State
	Compose     string
	Env         string
	Unit        string
	Admin       *AdminBootstrap
}

// Next turns a state and a command into a plan. It is pure: no clock, no
// randomness, no environment, no I/O. The CLI generates a secret before
// calling it, and a repair reuses the one already in the state.
func Next(state State, cmd Command) ([]Step, error) {
	switch cmd.Action {
	case ActionInstall:
		return nextInstall(state, cmd)
	case ActionLogs:
		return nextLogs(state)
	case ActionUninstall:
		return nextUninstall(state, cmd)
	case ActionAdminList:
		return adminListSteps(state)
	case ActionAdminResetPassword:
		return adminResetPasswordSteps(state, cmd)
	default:
		return nil, fmt.Errorf("unknown action %q", cmd.Action)
	}
}

func nextInstall(state State, cmd Command) ([]Step, error) {
	if state.Path == "" {
		return nil, errors.New("install state path is not set")
	}
	installed := state.Version != 0

	// A repair with no explicit runtime keeps the one it finds; a fresh plan
	// defaults to Docker. Only an explicit change can reach sameInstance.
	if cmd.Runtime == "" {
		cmd.Runtime = RuntimeDocker
		if installed {
			cmd.Runtime = state.runtimeOrDefault()
		}
	}

	if installed {
		if err := sameInstance(state, cmd); err != nil {
			return nil, err
		}
		// A repair keeps the recorded image tag: `install` is not `upgrade`,
		// and pulling a newer server than the binary that planned it is the
		// drift this design exists to prevent.
	} else {
		if strings.TrimSpace(cmd.Secret) == "" {
			return nil, errors.New("a secret is required to initialize an install")
		}
		if len(cmd.Secret) < 32 {
			return nil, errors.New("the secret must be at least 32 bytes")
		}
		if strings.TrimSpace(cmd.DataDir) == "" {
			return nil, errors.New("a data directory is required")
		}
		if strings.TrimSpace(cmd.InstanceName) == "" {
			return nil, errors.New("an instance name is required")
		}
		if strings.TrimSpace(cmd.Version) == "" {
			return nil, errors.New("a build version is required to pin the server image")
		}
		if cmd.ServerPort == 0 {
			cmd.ServerPort = 8080
		}

		state = State{
			Version:          StateVersion,
			InstanceName:     cmd.InstanceName,
			Mode:             cmd.Mode,
			Provider:         cmd.Provider,
			DataDir:          cmd.DataDir,
			ComposePath:      filepath.Join(cmd.DataDir, "docker-compose.yml"),
			ImageTag:         cmd.Version,
			ServerPort:       cmd.ServerPort,
			JWTSecret:        cmd.Secret,
			DatabasePassword: cmd.DBPassword,
			Path:             state.Path,
		}
		if state.Mode == ModePostgres && state.DatabasePassword == "" {
			return nil, errors.New("a database password is required for the postgres mode")
		}
		if (cmd.AdminEmail == "") != (cmd.AdminPassword == "") {
			return nil, errors.New("admin email and password must be provided together")
		}
	}

	if cmd.Runtime == RuntimeNative {
		if installed {
			return nativeInstallSteps(state, cmd, true), nil
		}
		native, err := nativeState(state, cmd)
		if err != nil {
			return nil, err
		}
		return nativeInstallSteps(native, cmd, false), nil
	}

	if err := state.Validate(); err != nil {
		return nil, err
	}
	compose, err := RenderCompose(state)
	if err != nil {
		return nil, err
	}

	steps := []Step{
		{Kind: StepWriteState, Description: "Write the install state", Init: !installed, State: state},
		{Kind: StepWriteCompose, Description: "Write the compose file", State: state, Compose: compose},
		{Kind: StepComposeUp, Description: "Start the server", State: state},
		{Kind: StepWaitReady, Description: "Wait for /readyz", State: state},
	}
	if !installed && cmd.AdminEmail != "" {
		steps = append(steps, Step{
			Kind:        StepCreateAdmin,
			Description: "Create the first admin",
			State:       state,
			Admin: &AdminBootstrap{
				Email:       cmd.AdminEmail,
				Password:    cmd.AdminPassword,
				DisplayName: cmd.AdminDisplayName,
			},
		})
	}
	return steps, nil
}

// nativeState is the document a fresh native install writes. It is built apart
// from the Docker state because it shares none of the compose fields: the
// binary, env file and unit replace the image and the compose file.
func nativeState(state State, cmd Command) (State, error) {
	if cmd.Mode != ModeSQLite {
		return State{}, errors.New("a native install runs SQLite only; use --mode postgres with --runtime docker")
	}
	if cmd.Provider != ProviderLAN {
		return State{}, fmt.Errorf("a native install exposes with provider lan only; %q needs --runtime docker", cmd.Provider)
	}
	if strings.TrimSpace(cmd.BinaryPath) == "" {
		return State{}, errors.New("a native install needs a server binary; pass --server-binary")
	}

	native := State{
		Version:      StateVersion,
		Runtime:      RuntimeNative,
		InstanceName: cmd.InstanceName,
		Mode:         ModeSQLite,
		Provider:     ProviderLAN,
		DataDir:      cmd.DataDir,
		BinaryPath:   cmd.BinaryPath,
		EnvPath:      NativeEnvPath,
		UnitPath:     DefaultUnitPath(cmd.InstanceName),
		ServerBind:   NativeLoopback,
		ImageTag:     cmd.Version,
		ServerPort:   cmd.ServerPort,
		JWTSecret:    cmd.Secret,
		Path:         state.Path,
	}
	if err := native.Validate(); err != nil {
		return State{}, err
	}
	return native, nil
}

// nativeInstallSteps is the same five steps for a fresh install and a repair;
// only the admin bootstrap is first-install-only.
func nativeInstallSteps(state State, cmd Command, installed bool) []Step {
	steps := []Step{
		{Kind: StepWriteState, Description: "Write the install state", Init: !installed, State: state},
		{Kind: StepEnsureUser, Description: "Create the service account and own the data directory", State: state},
		{Kind: StepWriteEnv, Description: "Write the environment file", State: state, Env: RenderNativeEnv(state)},
		{Kind: StepWriteUnit, Description: "Write the systemd unit", State: state, Unit: RenderNativeUnit(state)},
		{Kind: StepStartService, Description: "Start the service", State: state},
		{Kind: StepWaitReady, Description: "Wait for /readyz", State: state},
	}
	if !installed && cmd.AdminEmail != "" {
		steps = append(steps, Step{
			Kind:        StepCreateAdmin,
			Description: "Create the first admin",
			State:       state,
			Admin: &AdminBootstrap{
				Email:       cmd.AdminEmail,
				Password:    cmd.AdminPassword,
				DisplayName: cmd.AdminDisplayName,
			},
		})
	}
	return steps
}

// sameInstance refuses a command that would turn an existing SQLite instance
// into a Postgres one (or move its data directory) while the old containers
// still run. Changing shape is an uninstall and a fresh install.
func sameInstance(state State, cmd Command) error {
	if cmd.Runtime != "" && cmd.Runtime != state.runtimeOrDefault() {
		return fmt.Errorf("instance %q runs under %q; run tt uninstall before changing its runtime", state.InstanceName, state.runtimeOrDefault())
	}
	if cmd.Mode != "" && cmd.Mode != state.Mode {
		return fmt.Errorf("instance %q is already %s; run tt uninstall before changing mode", state.InstanceName, state.Mode)
	}
	if cmd.Provider != "" && cmd.Provider != state.Provider {
		return fmt.Errorf("instance %q already uses provider %s; run tt uninstall before changing provider", state.InstanceName, state.Provider)
	}
	if cmd.DataDir != "" && cmd.DataDir != state.DataDir {
		return fmt.Errorf("instance %q already uses data directory %s; run tt uninstall before moving it", state.InstanceName, state.DataDir)
	}
	if cmd.ServerPort != 0 && cmd.ServerPort != state.ServerPort {
		return fmt.Errorf("instance %q already uses port %d; run tt uninstall before changing it", state.InstanceName, state.ServerPort)
	}
	return nil
}

func nextLogs(state State) ([]Step, error) {
	if state.Version == 0 {
		return nil, errors.New("no installation found; run tt install")
	}
	if state.runtimeOrDefault() == RuntimeNative {
		return []Step{{Kind: StepServiceLogs, Description: "Follow the service logs", State: state}}, nil
	}
	return []Step{{Kind: StepComposeLogs, Description: "Follow the server logs", State: state}}, nil
}

func nextUninstall(state State, cmd Command) ([]Step, error) {
	if state.Version == 0 {
		return nil, errors.New("no installation found; run tt install")
	}
	if cmd.ConfirmName != state.InstanceName {
		return nil, fmt.Errorf("the typed instance name does not match %q; refusing to uninstall", state.InstanceName)
	}
	if state.runtimeOrDefault() == RuntimeNative {
		// The data directory survives: uninstalling the service is not deleting
		// the customer's database.
		return []Step{
			{Kind: StepStopService, Description: "Stop and disable the service", State: state},
			{Kind: StepRemoveEnv, Description: "Remove the environment file", State: state},
			{Kind: StepRemoveState, Description: "Remove the install state", State: state},
		}, nil
	}
	return []Step{
		{Kind: StepComposeDown, Description: "Stop and remove the containers", State: state},
		{Kind: StepRemoveState, Description: "Remove the install state", State: state},
	}, nil
}

// StateFromSteps returns the state a plan would persist. Every step carries
// the same state; the write-state step is the authoritative one.
func StateFromSteps(steps []Step) State {
	for _, step := range steps {
		if step.Kind == StepWriteState {
			return step.State
		}
	}
	return State{}
}

// ComposeFromSteps returns the rendered compose from a plan, for --dry-run.
func ComposeFromSteps(steps []Step) string {
	for _, step := range steps {
		if step.Compose != "" {
			return step.Compose
		}
	}
	return ""
}
