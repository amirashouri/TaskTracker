package main

import (
	"context"
	"fmt"
	"io"
)

// runAgent is the command group for the machine that runs agent work. It is
// deliberately independent of the install state: a runner lives wherever the code
// is, which is usually a developer machine rather than the server host.
func runAgent(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		agentUsage(stderr)
		return 2
	}

	switch args[0] {
	case "connect":
		return runAgentConnect(ctx, args[1:], stdin, stdout, stderr)
	case "run":
		return runAgentRun(ctx, args[1:], stdout, stderr)
	case "status":
		return runAgentStatus(ctx, args[1:], stdout, stderr)
	case "disconnect":
		return runAgentDisconnect(ctx, args[1:], stdout, stderr)
	case "help", "--help", "-h":
		agentUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "tt agent: unknown command %q\n", args[0])
		agentUsage(stderr)
		return 2
	}
}

func agentUsage(w io.Writer) {
	fmt.Fprint(w, `tt agent — connect this machine to a server and run agent work

Usage:
  tt agent connect [--server URL] [--email EMAIL] [--name NAME]
                   [--provider PROVIDER] [--model MODEL] [--base-url URL]
                   [--api-key KEY] [--pairing-code CODE] [--force]
  tt agent run [--provider PROVIDER] [--model MODEL] [--base-url URL]
               [--api-key KEY] [--workspace DIR] [--max-steps N]
               [--once] [--wait SECONDS] [--heartbeat DURATION]
  tt agent status
  tt agent disconnect

--pairing-code redeems a one-use ttpc_… code minted in the app and needs no
email or password; --email is the older machine-wide registration. Pass one or
the other, not both. Without --server, connect asks for the server URL.

Providers: echo runs no model; openai-compatible reaches Ollama, LM Studio,
llama.cpp, vLLM and OpenAI at one endpoint; anthropic speaks the Messages API.

tt agent run edits the directory named by --workspace and runs commands there.
Run it on the checkout the task is about, and review its diff before you commit.

The runner talks to your TaskTracker server; the model it uses is reached from
this machine. No provider key is ever sent to the server.

Config lives in $TT_AGENT_CONFIG, or the per-user config directory as
tasktracker/agent.json, and is written 0600.
`)
}
