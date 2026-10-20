package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestPrompterAskUsesTheDefaultForAnEmptyAnswer(t *testing.T) {
	var out bytes.Buffer
	p := newPrompter(strings.NewReader("\n"), &out)

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
	p := newPrompter(strings.NewReader("postgres\n"), &out)

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
	p := newPrompter(strings.NewReader("\nadmin@example.com\n"), &out)

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
	p := newPrompter(strings.NewReader(" pass word \n"), &out)

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
	p := newPrompter(strings.NewReader("\npassword123\n"), &out)

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
	p := newPrompter(strings.NewReader(""), &out)

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
			p := newPrompter(strings.NewReader(tc.input), &out)
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

func TestIsTerminalRejectsNonFiles(t *testing.T) {
	if isTerminal(strings.NewReader("")) {
		t.Error("isTerminal accepted a strings.Reader")
	}
}
