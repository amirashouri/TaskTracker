package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// prompter is the wizard's only source of answers. Tests provide a scripted
// implementation; production reads the terminal without a TUI dependency.
type prompter interface {
	Ask(label, defaultValue string) (string, error)
	AskRequired(label string) (string, error)
	Secret(label string) (string, error)
	Confirm(label string, defaultYes bool) (bool, error)
}

type stdinPrompter struct {
	reader *bufio.Reader
	out    io.Writer
}

func newPrompter(in io.Reader, out io.Writer) *stdinPrompter {
	return &stdinPrompter{reader: bufio.NewReader(in), out: out}
}

func (p *stdinPrompter) Ask(label, defaultValue string) (string, error) {
	fmt.Fprint(p.out, label)
	if defaultValue != "" {
		fmt.Fprintf(p.out, " [%s]", defaultValue)
	}
	fmt.Fprint(p.out, ": ")

	line, err := p.reader.ReadString('\n')
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
		line, err := p.reader.ReadString('\n')
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
		line, err := p.reader.ReadString('\n')
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
