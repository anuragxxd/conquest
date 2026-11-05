// Package routing sends each task to Conquest's local Kubernetes backend or
// to a remote TES endpoint, per operator rules and client hints.
//
// The local backend is the default. A task goes to an endpoint only when a
// rule, or a hint for a client-selectable endpoint, names it and the
// endpoint accepts the run's data class and the task's resources. Anything
// else stays local.
package routing

import (
	"fmt"
	"os"
	"path"

	"sigs.k8s.io/yaml"

	"github.com/anuragxxd/conquest/internal/backend/remote"
)

// Local names the local Kubernetes backend in rules, hints and records.
const Local = "local"

// Tag keys read from runs (WES tags) and tasks (TES tags).
const (
	// HintTag names the backends a client wants, in order, comma-separated
	// (e.g. "funnel-a,local"). A task tag overrides a run tag.
	HintTag = "conquest.tes_backend"
	// DataClassTag classifies a run's data; endpoints list the classes
	// they may receive. Runs without it are DefaultDataClass.
	DataClassTag = "conquest.data_class"
	// DefaultDataClass is the class of runs without DataClassTag.
	DefaultDataClass = "unclassified"
)

// Config is the federation configuration file (CONQUEST_FEDERATION_CONFIG).
type Config struct {
	// GatewayID names this Conquest in Via headers of forwarded requests;
	// requests carrying it are rejected as loops. Default: "conquest".
	GatewayID string                  `json:"gateway_id,omitempty"`
	Endpoints []remote.EndpointConfig `json:"endpoints"`
	Routing   RoutingConfig           `json:"routing"`
}

// RoutingConfig holds the operator rules.
type RoutingConfig struct {
	// Rules are tried in order; the first that matches a task decides its
	// candidates.
	Rules []Rule `json:"rules,omitempty"`
	// ClientHints enables HintTag. Hints only narrow and reorder what rules
	// and client_selectable endpoints allow.
	ClientHints bool `json:"client_hints,omitempty"`
}

// Rule sends matching tasks to its backends, in order.
type Rule struct {
	Name  string `json:"name"`
	Match Match  `json:"match"`
	// Backends are endpoint names, or "local", in order of preference.
	Backends []string `json:"backends"`
	// NoLocalFallback stops a task that every listed backend refused or
	// excluded from falling back to the local backend.
	NoLocalFallback bool `json:"no_local_fallback,omitempty"`
}

// Match selects tasks. All set fields must match; an empty Match matches
// every task.
type Match struct {
	// Engines matches the run's workflow engine (nextflow, snakemake, wdl).
	Engines []string `json:"engines,omitempty"`
	// TaskNames are glob patterns (path.Match) on the TES task name, e.g.
	// "*:FASTQC (*" for a Nextflow process.
	TaskNames []string `json:"task_names,omitempty"`
	// RunTags must all be present on the run with these values.
	RunTags map[string]string `json:"run_tags,omitempty"`
}

// LoadConfig reads and validates a federation configuration file.
func LoadConfig(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read federation config: %w", err)
	}
	var cfg Config
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse federation config %s: %w", file, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("federation config %s: %w", file, err)
	}
	return &cfg, nil
}

// Validate checks the configuration and fills in defaults.
func (c *Config) Validate() error {
	if c.GatewayID == "" {
		c.GatewayID = "conquest"
	}
	names := map[string]bool{}
	for i := range c.Endpoints {
		ep := &c.Endpoints[i]
		if err := ep.Validate(); err != nil {
			return err
		}
		if names[ep.Name] {
			return fmt.Errorf("endpoint %q defined twice", ep.Name)
		}
		names[ep.Name] = true
	}
	for i, r := range c.Routing.Rules {
		if r.Name == "" {
			return fmt.Errorf("rule %d has no name", i)
		}
		if len(r.Backends) == 0 {
			return fmt.Errorf("rule %s lists no backends", r.Name)
		}
		for _, b := range r.Backends {
			if b != Local && !names[b] {
				return fmt.Errorf("rule %s: unknown backend %q", r.Name, b)
			}
		}
		for _, p := range r.Match.TaskNames {
			if _, err := path.Match(p, ""); err != nil {
				return fmt.Errorf("rule %s: bad task name pattern %q: %w", r.Name, p, err)
			}
		}
	}
	return nil
}
