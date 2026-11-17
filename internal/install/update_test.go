package install

import (
	"strings"
	"testing"
)

func TestDockerUpdatePullsBeforeRecordingTheNewTag(t *testing.T) {
	state := testState(t)

	steps, err := Next(state, Command{Action: ActionUpdate, Version: "v1.4.0"})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	want := []StepKind{StepPullImage, StepWriteState, StepWriteCompose, StepComposeUp, StepWaitReady}
	if len(steps) != len(want) {
		t.Fatalf("steps = %d, want %d: %+v", len(steps), len(want), steps)
	}
	for i, kind := range want {
		if steps[i].Kind != kind {
			t.Errorf("step %d = %q, want %q", i, steps[i].Kind, kind)
		}
	}

	if steps[0].Image != ImageRef("v1.4.0") {
		t.Errorf("pull reference = %q, want the new tag", steps[0].Image)
	}
	if final := StateFromSteps(steps); final.ImageTag != "v1.4.0" {
		t.Errorf("recorded tag = %q, want v1.4.0", final.ImageTag)
	}
	compose := ComposeFromSteps(steps)
	if !strings.Contains(compose, ImageRef("v1.4.0")) {
		t.Errorf("compose does not pin the new image tag:\n%s", compose)
	}
}

func TestNativeUpdateFetchesThenRestarts(t *testing.T) {
	state := nativeTestState(t)

	steps, err := Next(state, Command{
		Action:          ActionUpdate,
		Version:         "v1.4.0",
		ServerBinaryURL: "https://example.com/tt/server",
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}

	want := []StepKind{StepFetchBinary, StepWriteState, StepRestartService, StepWaitReady}
	if len(steps) != len(want) {
		t.Fatalf("steps = %d, want %d: %+v", len(steps), len(want), steps)
	}
	for i, kind := range want {
		if steps[i].Kind != kind {
			t.Errorf("step %d = %q, want %q", i, steps[i].Kind, kind)
		}
	}
	if steps[0].SourceURL != "https://example.com/tt/server" {
		t.Errorf("fetch URL = %q", steps[0].SourceURL)
	}
	if steps[0].State.BinaryPath != state.BinaryPath {
		t.Errorf("fetch destination = %q, want the supervised binary %q", steps[0].State.BinaryPath, state.BinaryPath)
	}

	final := StateFromSteps(steps)
	if final.ImageTag != "v1.4.0" {
		t.Errorf("recorded tag = %q, want v1.4.0", final.ImageTag)
	}
	if final.BinaryPath != state.BinaryPath {
		t.Errorf("binary path moved from %q to %q", state.BinaryPath, final.BinaryPath)
	}
}

// A native update without a new tag keeps the version it has: the binary is the
// thing that changed, and inventing a tag from "latest" would be a claim tt
// cannot verify.
func TestNativeUpdateKeepsTheRecordedVersionWithoutOne(t *testing.T) {
	state := nativeTestState(t)

	steps, err := Next(state, Command{
		Action:          ActionUpdate,
		ServerBinaryURL: "https://example.com/tt/server",
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if final := StateFromSteps(steps); final.ImageTag != state.ImageTag {
		t.Errorf("recorded tag = %q, want the existing %q", final.ImageTag, state.ImageTag)
	}
}

func TestUpdateRefusesWhatItCannotApply(t *testing.T) {
	docker := testState(t)
	native := nativeTestState(t)

	cases := []struct {
		name  string
		state State
		cmd   Command
		want  string
	}{
		{
			name:  "docker without a version",
			state: docker,
			cmd:   Command{Action: ActionUpdate},
			want:  "--version",
		},
		{
			name:  "docker with a binary URL",
			state: docker,
			cmd:   Command{Action: ActionUpdate, Version: "v1.4.0", ServerBinaryURL: "https://example.com/tt/server"},
			want:  "native instance",
		},
		{
			name:  "native without a URL",
			state: native,
			cmd:   Command{Action: ActionUpdate, Version: "v1.4.0"},
			want:  "--server-binary-url",
		},
		{
			name:  "native over http",
			state: native,
			cmd:   Command{Action: ActionUpdate, ServerBinaryURL: "http://example.com/tt/server"},
			want:  "https",
		},
		{
			name:  "no installation",
			state: State{},
			cmd:   Command{Action: ActionUpdate, Version: "v1.4.0"},
			want:  "no installation found",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Next(testCase.state, testCase.cmd)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Next = %v, want an error containing %q", err, testCase.want)
			}
		})
	}
}
