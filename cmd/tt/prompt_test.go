package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestPrompterAskUsesTheDefaultForAnEmptyAnswer(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(context.Background(), strings.NewReader("\n"), &out)

	value, err := p.Ask("Data directory", "/var/lib/tasktracker")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if value != "/var/lib/tasktracker" {
		t.Errorf("value = %q, want the default", value)
	}
}

func TestPrompterAskReturnsAValue(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(context.Background(), strings.NewReader("postgres\n"), &out)

	value, err := p.Ask("Database", "sqlite")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if value != "postgres" {
		t.Errorf("value = %q, want postgres", value)
	}
}

func TestPrompterAskRequiredRepromptsOnEmpty(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(context.Background(), strings.NewReader("\nadmin@example.com\n"), &out)

	value, err := p.AskRequired("Admin email")
	if err != nil {
		t.Fatalf("AskRequired: %v", err)
	}
	if value != "admin@example.com" {
		t.Errorf("value = %q, want the second answer", value)
	}
	if !strings.Contains(out.String(), "A value is required") {
		t.Errorf("output = %q, want a required-value hint", out.String())
	}
}

func TestPrompterSecretPreservesSpaces(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(context.Background(), strings.NewReader(" pass word \n"), &out)

	value, err := p.Secret("Admin password")
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if value != " pass word " {
		t.Errorf("value = %q, want the spaces preserved", value)
	}
}

func TestPrompterSecretRepromptsOnEmpty(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(context.Background(), strings.NewReader("\npassword123\n"), &out)

	value, err := p.Secret("Admin password")
	if err != nil {
		t.Fatalf("Secret: %v", err)
	}
	if value != "password123" {
		t.Errorf("value = %q, want the second answer", value)
	}
	if !strings.Contains(out.String(), "A value is required") {
		t.Errorf("output = %q, want a required-value hint", out.String())
	}
}

func TestPrompterAskEOFIsAnError(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(context.Background(), strings.NewReader(""), &out)

	if _, err := p.Ask("Name", "default"); !errors.Is(err, io.EOF) {
		t.Errorf("Ask error = %v, want EOF", err)
	}
}

func TestPrompterConfirm(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
		wantErr    bool
	}{
		{name: "yes", input: "yes\n", want: true},
		{name: "no", input: "no\n", want: false},
		{name: "empty defaults yes", input: "\n", defaultYes: true, want: true},
		{name: "empty defaults no", input: "\n", defaultYes: false, want: false},
		{name: "invalid then no", input: "maybe\nn\n", want: false},
		{name: "eof", input: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			p := newPrompter(context.Background(), strings.NewReader(tc.input), &out)
			got, err := p.Confirm("Continue?", tc.defaultYes)
			if tc.wantErr {
				if err == nil {
					t.Fatal("Confirm succeeded on EOF")
				}
				return
			}
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if got != tc.want {
				t.Errorf("Confirm = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPrompterStopsOnCancellation(t *testing.T) {
	prompts := map[string]func(p *stdinPrompter) error{
		"Ask": func(p *stdinPrompter) error {
			_, err := p.Ask("Name", "default")
			return err
		},
		"AskRequired": func(p *stdinPrompter) error {
			_, err := p.AskRequired("Email")
			return err
		},
		"Secret": func(p *stdinPrompter) error {
			_, err := p.Secret("Password")
			return err
		},
		"Confirm": func(p *stdinPrompter) error {
			_, err := p.Confirm("Continue?", true)
			return err
		},
	}

	for name, prompt := range prompts {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			reader, writer := io.Pipe()
			t.Cleanup(func() { writer.Close() })

			var out bytes.Buffer
			p := newPrompter(ctx, reader, &out)

			done := make(chan error, 1)
			go func() { done <- prompt(p) }()
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, errInterrupted) {
					t.Fatalf("error = %v, want errInterrupted", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("prompt ignored the cancellation and stayed blocked")
			}
		})
	}
}

func TestIsTerminalRejectsNonFiles(t *testing.T) {
	if isTerminal(strings.NewReader("")) {
		t.Error("isTerminal accepted a strings.Reader")
	}
}
