package runner

import (
	"errors"
	"fmt"
	"strings"
)

// Provider names this build can run. `echo` needs no model, which is what makes
// the worker testable; the other two are the real ones.
const (
	ProviderEcho      = "echo"
	ProviderOpenAI    = "openai-compatible"
	ProviderAnthropic = "anthropic"
)

// ProviderList is the one string every refusal quotes, so the list cannot drift
// between the CLI, the registry and the docs.
const ProviderList = ProviderEcho + ", " + ProviderOpenAI + ", " + ProviderAnthropic

// Default endpoints. Ollama is the local default because it is the one a laptop
// already has; a hosted OpenAI key needs only --base-url https://api.openai.com/v1.
const (
	defaultOpenAIBase    = "http://localhost:11434/v1"
	defaultAnthropicBase = "https://api.anthropic.com"
)

// ModelConfig is the runner-side half of a provider: the endpoint, the model and
// the key all live on the machine that runs the agent. None of it is registered
// with the server, which is the milestone's data-ownership rule.
type ModelConfig struct {
	Provider  string
	Model     string
	BaseURL   string
	APIKey    string
	Workspace string
	MaxSteps  int
}

// KnownProvider reports whether a name is one this build runs. Pairing uses it so
// a typo fails while the user is looking at the command, not at the first run.
func KnownProvider(name string) bool {
	switch strings.TrimSpace(name) {
	case ProviderEcho, ProviderOpenAI, ProviderAnthropic:
		return true
	default:
		return false
	}
}

// NewProvider builds the provider a run will use. A missing model or key is
// refused here rather than at the first HTTP call, when the run has already been
// claimed and would have to be failed with a lease to clean up.
func NewProvider(cfg ModelConfig) (Provider, error) {
	name := strings.TrimSpace(cfg.Provider)
	if name == "" {
		name = ProviderEcho
	}
	cfg.Provider = name
	if strings.TrimSpace(cfg.Workspace) == "" {
		cfg.Workspace = "."
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = defaultMaxSteps
	}

	switch name {
	case ProviderEcho:
		return EchoProvider{}, nil
	case ProviderOpenAI:
		if strings.TrimSpace(cfg.Model) == "" {
			return nil, fmt.Errorf("provider %s needs a model; pass --model or set it with tt agent connect --model", name)
		}
		cfg.BaseURL = endpointOrDefault(cfg.BaseURL, defaultOpenAIBase)
		return &OpenAIProvider{cfg: cfg}, nil
	case ProviderAnthropic:
		if strings.TrimSpace(cfg.Model) == "" {
			return nil, fmt.Errorf("provider %s needs a model; pass --model or set it with tt agent connect --model", name)
		}
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, fmt.Errorf("provider %s needs an API key; pass --api-key or set TT_AGENT_API_KEY", name)
		}
		cfg.BaseURL = endpointOrDefault(cfg.BaseURL, defaultAnthropicBase)
		return &AnthropicProvider{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q; this build supports %s", name, ProviderList)
	}
}

func endpointOrDefault(raw, fallback string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

// errNoWorkspace is what a model provider reports when it cannot resolve the
// directory it was told to work in.
var errNoWorkspace = errors.New("the workspace directory is not usable")
