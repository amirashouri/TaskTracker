package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// errInterrupted is a prompt abandoned because a signal cancelled the run. The
// caller turns it into the shell's exit status for SIGINT rather than an error.
var errInterrupted = errors.New("interrupted")

// prompter is the wizard's only source of answers. Tests provide a scripted
// implementation; production reads the terminal without a TUI dependency.
type prompter interface {
	Ask(label, defaultValue string) (string, error)
	AskRequired(label string) (string, error)
	Secret(label string) (string, error)
	Confirm(label string, defaultYes bool) (bool, error)
}

type stdinPrompter struct {
	ctx    context.Context
	reader *bufio.Reader
	out    io.Writer
}

// newPrompter carries the run's context because main installs a signal handler:
// SIGINT no longer terminates the process by itself, so a read that ignores the
// context is a CLI that cannot be interrupted while it waits for an answer.
func newPrompter(ctx context.Context, in io.Reader, out io.Writer) *stdinPrompter {
	if ctx == nil {
		ctx = context.Background()
	}
	return &stdinPrompter{ctx: ctx, reader: bufio.NewReader(in), out: out}
}

type lineResult struct {
	line string
	err  error
}

// readLine returns one line, or errInterrupted once ctx is cancelled. A blocked
// read on a terminal cannot be unblocked portably, so it is left to finish on
// its own while the CLI exits.
func (p *stdinPrompter) readLine() (string, error) {
	lines := make(chan lineResult, 1)
	go func() {
		line, err := p.reader.ReadString('\n')
		lines <- lineResult{line: line, err: err}
	}()

	select {
	case result := <-lines:
		return result.line, result.err
	case <-p.ctx.Done():
		return "", errInterrupted
	}
}

func (p *stdinPrompter) Ask(label, defaultValue string) (string, error) {
	fmt.Fprint(p.out, label)
	if defaultValue != "" {
		fmt.Fprintf(p.out, " [%s]", defaultValue)
	}
	fmt.Fprint(p.out, ": ")

	line, err := p.readLine()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return "", err
		}
		if strings.TrimSpace(line) == "" {
			return "", err
		}
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func (p *stdinPrompter) AskRequired(label string) (string, error) {
	for {
		value, err := p.Ask(label, "")
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(p.out, "A value is required.")
	}
}

// Secret preserves leading and trailing spaces: trimming a password silently
// changes it. It does not hide the input; that needs a terminal dependency.
func (p *stdinPrompter) Secret(label string) (string, error) {
	for {
		fmt.Fprintf(p.out, "%s: ", label)
		line, err := p.readLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return "", err
			}
			if line == "" {
				return "", err
			}
		}
		value := strings.TrimSuffix(line, "\n")
		value = strings.TrimSuffix(value, "\r")
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(p.out, "A value is required.")
	}
}

func (p *stdinPrompter) Confirm(label string, defaultYes bool) (bool, error) {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	for {
		fmt.Fprintf(p.out, "%s %s: ", label, suffix)
		line, err := p.readLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return false, err
			}
			if strings.TrimSpace(line) == "" {
				return false, err
			}
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "":
			return defaultYes, nil
		default:
			fmt.Fprintln(p.out, "Answer yes or no.")
		}
	}
}

func isTerminal(in io.Reader) bool {
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
