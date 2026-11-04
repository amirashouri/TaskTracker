package install

import (
	"fmt"
	"path/filepath"
	"strings"
)

// A native instance has one layout so the CLI and docs/SELF-HOSTING.md cannot
// disagree about where the unit, the env file, or the binary lives.
const (
	// NativeServiceUser owns the data directory and runs the unit. It is the
	// account docs/SELF-HOSTING.md §3 creates, so a manual install and a
	// `tt install --runtime native` land on the same host shape.
	NativeServiceUser = "tasktracker"
	// NativeEnvPath holds the server's environment, including JWT_SECRET. It is
	// read by systemd as root, so 0600 is enough.
	NativeEnvPath = "/etc/tasktracker/secrets.env"
	// NativeBinaryPath is where the release bundle and the docs put the server.
	NativeBinaryPath = "/opt/tasktracker/server"
	// NativeLoopback is the bind a fresh native instance gets. `tt expose`
	// widens it to NativeAllInterfaces.
	NativeLoopback      = "127.0.0.1"
	NativeAllInterfaces = "0.0.0.0"
)

// DefaultUnitPath is the unit file for one instance. The instance name becomes
// the unit name, so two instances on one host do not collide.
func DefaultUnitPath(instance string) string {
	return filepath.Join("/etc/systemd/system", instance+".service")
}

// ServiceName is the systemd unit an instance runs under.
func (s State) ServiceName() string {
	return s.InstanceName + ".service"
}

// DatabaseFile is the SQLite file inside the data directory.
func (s State) DatabaseFile() string {
	return filepath.Join(s.DataDir, "tasktracker.db")
}

// Bind is the native bind address, defaulting to loopback. A Docker install
// keeps this empty: its publish lives in the compose file.
func (s State) Bind() string {
	if strings.TrimSpace(s.ServerBind) == "" {
		return NativeLoopback
	}
	return s.ServerBind
}

// RenderNativeEnv renders the EnvironmentFile the unit reads. It is a text file
// of KEY=value lines with no quoting, so every value it carries is a path, an
// address, or a secret without spaces.
func RenderNativeEnv(state State) string {
	lines := []string{
		"# Rendered by tt; re-run `tt install` to repair. Do not edit by hand.",
		"JWT_SECRET=" + state.JWTSecret,
		"DATABASE_URL=sqlite://" + state.DatabaseFile(),
		"ATTACHMENT_ROOT=" + filepath.Join(state.DataDir, "blobstore"),
		"SERVER_BIND=" + state.Bind(),
		fmt.Sprintf("SERVER_PORT=%d", state.ServerPort),
		"LOG_LEVEL=info",
		"",
	}
	return strings.Join(lines, "\n")
}

// RenderNativeUnit renders the hardened systemd unit for one instance. It is
// the shape of deploy/tasktracker.service with the paths this instance owns.
func RenderNativeUnit(state State) string {
	return fmt.Sprintf(`[Unit]
Description=TaskTracker server (%[1]s)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%[2]s
Group=%[2]s
WorkingDirectory=%[3]s
ExecStart=%[4]s serve

# Secrets live here: mode 0600, owned by root. systemd reads it as root and
# hands the values to the service, so it is not world- or group-readable.
EnvironmentFile=%[5]s

Restart=on-failure
RestartSec=5s
LimitNOFILE=65536

# The service needs no privileges and no writable path outside its data
# directory. ProtectSystem=strict makes the rest of the filesystem read-only.
ReadWritePaths=%[3]s

NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
LockPersonality=true
CapabilityBoundingSet=
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
`, state.InstanceName, NativeServiceUser, state.DataDir, state.BinaryPath, state.EnvPath)
}
