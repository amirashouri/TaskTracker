package install

import "embed"

// assets carries everything the CLI needs when it runs from a directory the
// customer downloaded it into. The systemd unit is also kept in deploy/ for
// the self-hosting guide; a test keeps the two copies identical.
//
//go:embed embed
var assets embed.FS
