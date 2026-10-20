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
	ActionInstall   Action = "install"
	ActionStatus    Action = "status"
	ActionDoctor    Action = "doctor"
	ActionLogs      Action = "logs"
	ActionUninstall Action = "uninstall"
)

// Command is everything the plan may read besides the state. Defaults are
// resolved by the CLI, not here, so Next stays a pure function.
type Command struct {
	Action       Action
	Mode         Mode
	Provider     Provider
	DataDir      string
	InstanceName string
	Version      string
	Secret       string
	DBPassword   string
	ConfirmName  string
	ServerPort   int
}

// StepKind names one unit of work. The plan returns steps; the runner is the
// only code that knows how to execute one.
type StepKind string

const (
	StepWriteState   StepKind = "write-state"
	StepWriteCompose StepKind = "write-compose"
	StepComposeUp    StepKind = "compose-up"
	StepWaitReady    StepKind = "wait-ready"
	StepComposePS    StepKind = "compose-ps"
	StepComposeLogs  StepKind = "compose-logs"
	StepComposeDown  StepKind = "compose-down"
	StepRemoveState  StepKind = "remove-state"
)

// Step is one action in a plan. Init marks the step that creates the state and
// secret, so a repair can be asserted to contain none of them.
type Step struct {
	Kind        StepKind
	Description string
	Init        bool
	State       State
	Compose     string
}

// Next turns a state and a command into a plan. It is pure: no clock, no
// randomness, no environment, no I/O. The CLI generates a secret before
// calling it, and a repair reuses the one already in the state.
func Next(state State, cmd Command) ([]Step, error) {
	switch cmd.Action {
	case ActionInstall:
		return nextInstall(state, cmd)
	case ActionStatus:
		return nextStatus(state)
	case ActionDoctor:
		return nextDoctor(state)
	case ActionLogs:
		return nextLogs(state)
	case ActionUninstall:
		return nextUninstall(state, cmd)
	default:
		return nil, fmt.Errorf("unknown action %q", cmd.Action)
	}
}

func nextInstall(state State, cmd Command) ([]Step, error) {
	if state.Path == "" {
		return nil, errors.New("install state path is not set")
	}
	installed := state.Version != 0

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
	}

	if err := state.Validate(); err != nil {
		return nil, err
	}
	compose, err := RenderCompose(state)
	if err != nil {
		return nil, err
	}

	return []Step{
		{Kind: StepWriteState, Description: "Write the install state", Init: !installed, State: state},
		{Kind: StepWriteCompose, Description: "Write the compose file", State: state, Compose: compose},
		{Kind: StepComposeUp, Description: "Start the server", State: state},
		{Kind: StepWaitReady, Description: "Wait for /readyz", State: state},
	}, nil
}

// sameInstance refuses a command that would turn an existing SQLite instance
// into a Postgres one (or move its data directory) while the old containers
// still run. Changing shape is an uninstall and a fresh install.
func sameInstance(state State, cmd Command) error {
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

func nextStatus(state State) ([]Step, error) {
	if state.Version == 0 {
		return nil, errors.New("no installation found; run tt install")
	}
	return []Step{{Kind: StepComposePS, Description: "Show the container state", State: state}}, nil
}

func nextDoctor(state State) ([]Step, error) {
	if state.Version == 0 {
		return nil, errors.New("no installation found; run tt install")
	}
	// T9.6 adds the network, TLS, DNS, and clock checks. Until then doctor
	// tells the operator the same container-state truth as status rather than
	// inventing a green check it cannot prove.
	return []Step{{Kind: StepComposePS, Description: "Show the container state", State: state}}, nil
}

func nextLogs(state State) ([]Step, error) {
	if state.Version == 0 {
		return nil, errors.New("no installation found; run tt install")
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
