package routing

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/anuragxxd/conquest/internal/backend/remote"
	"github.com/anuragxxd/conquest/internal/domain"
)

// Record is how a task was routed. It is stored with the task.
type Record struct {
	DecidedAt time.Time `json:"decided_at"`
	// LocalOnly says why the task could only run locally, if so.
	LocalOnly string `json:"local_only,omitempty"`
	// Rule is the operator rule that matched, if any.
	Rule string `json:"rule,omitempty"`
	// Hint is the client's hint, if hints are enabled and one was given.
	Hint []string `json:"hint,omitempty"`
	// DataClass is the run's data class.
	DataClass string `json:"data_class"`
	// Candidates are the backends to try, in order.
	Candidates []string `json:"candidates"`
	// Excluded are backends a rule or hint named but the task can't use.
	Excluded []Exclusion `json:"excluded,omitempty"`
	// Attempts are the submissions made, in order.
	Attempts []Attempt `json:"attempts,omitempty"`
	// Chosen is the backend that took the task.
	Chosen string `json:"chosen,omitempty"`
}

// Exclusion is a backend left out of the candidates, and why.
type Exclusion struct {
	Backend string `json:"backend"`
	Reason  string `json:"reason"`
}

// Attempt is one submission.
type Attempt struct {
	Backend string `json:"backend"`
	// Outcome is accepted, refused (certainly not created) or unknown.
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// runInfo is what routing needs to know about the task's run.
type runInfo struct {
	Engine string
	Tags   map[string]string
}

// taskRequest is the part of a stored TES request routing looks at.
type taskRequest struct {
	Name   string            `json:"name"`
	Tags   map[string]string `json:"tags"`
	Inputs []struct {
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"inputs"`
	Outputs []struct {
		URL string `json:"url"`
	} `json:"outputs"`
	Resources *struct {
		CPUCores *int32   `json:"cpu_cores"`
		RAMGb    *float64 `json:"ram_gb"`
	} `json:"resources"`
}

// router decides where tasks go. It holds no state between decisions.
type router struct {
	cfg       *Config
	endpoints map[string]*remote.EndpointConfig
	bucket    string // Conquest's S3 bucket: the only one endpoints can use
}

func newRouter(cfg *Config, bucket string) *router {
	r := &router{cfg: cfg, endpoints: map[string]*remote.EndpointConfig{}, bucket: bucket}
	for i := range cfg.Endpoints {
		r.endpoints[cfg.Endpoints[i].Name] = &cfg.Endpoints[i]
	}
	return r
}

// decide returns the routing record for a task, with its candidates.
func (r *router) decide(task *domain.Task, run runInfo, now time.Time) (*Record, error) {
	rec := &Record{DecidedAt: now.UTC(), DataClass: run.Tags[DataClassTag]}
	if rec.DataClass == "" {
		rec.DataClass = DefaultDataClass
	}

	var req taskRequest
	if len(task.TaskRequestPayload) > 0 {
		if err := json.Unmarshal(task.TaskRequestPayload, &req); err != nil {
			return nil, fmt.Errorf("decode stored task: %w", err)
		}
	}
	if reason := r.localOnly(task, &req); reason != "" {
		rec.LocalOnly = reason
		rec.Candidates = []string{Local}
		return rec, nil
	}

	var candidates []string
	fallback := true
	rule := r.matchRule(task, &req, run)
	if rule != nil {
		rec.Rule = rule.Name
		candidates = slices.Clone(rule.Backends)
		fallback = !rule.NoLocalFallback
	}

	if r.cfg.Routing.ClientHints {
		hint := parseHint(req.Tags[HintTag])
		if hint == nil {
			hint = parseHint(run.Tags[HintTag])
		}
		if hint != nil {
			rec.Hint = hint
			candidates = r.applyHint(rec, rule, candidates, hint)
		}
	}

	for _, name := range candidates {
		if slices.Contains(rec.Candidates, name) {
			continue
		}
		if name != Local {
			if reason := r.ineligible(name, rec.DataClass, &req, task.TaskRequestPayload); reason != "" {
				rec.Excluded = append(rec.Excluded, Exclusion{Backend: name, Reason: reason})
				continue
			}
		}
		rec.Candidates = append(rec.Candidates, name)
	}
	if fallback && !slices.Contains(rec.Candidates, Local) {
		rec.Candidates = append(rec.Candidates, Local)
	}
	if len(rec.Candidates) == 0 {
		return rec, fmt.Errorf("rule %s allows no backend for this task and forbids running it locally", rec.Rule)
	}
	return rec, nil
}

// applyHint narrows and reorders candidates by a client hint. Without a
// matching rule, a hint may name client-selectable endpoints.
func (r *router) applyHint(rec *Record, rule *Rule, candidates, hint []string) []string {
	var out []string
	for _, name := range hint {
		switch {
		case name == Local:
			out = append(out, name)
		case rule != nil && !slices.Contains(candidates, name):
			rec.Excluded = append(rec.Excluded, Exclusion{Backend: name, Reason: "not allowed by rule " + rule.Name})
		case rule == nil && r.endpoints[name] == nil:
			rec.Excluded = append(rec.Excluded, Exclusion{Backend: name, Reason: "unknown endpoint"})
		case rule == nil && !r.endpoints[name].ClientSelectable:
			rec.Excluded = append(rec.Excluded, Exclusion{Backend: name, Reason: "not client-selectable"})
		default:
			out = append(out, name)
		}
	}
	return out
}

// localOnly returns why a task can only run on the local backend, or "".
// Remote endpoints get no shared filesystem and only Conquest's bucket.
func (r *router) localOnly(task *domain.Task, req *taskRequest) string {
	if task.Workspace != nil {
		return "run uses a shared workspace"
	}
	if req.Tags["conquest_workspace_scope"] == "shared" {
		return "task uses the shared workspace"
	}
	for _, in := range req.Inputs {
		if in.URL == "" {
			continue // inline content
		}
		if reason := r.remoteURL(in.URL, false); reason != "" {
			return "input " + reason
		}
	}
	for _, out := range req.Outputs {
		if reason := r.remoteURL(out.URL, true); reason != "" {
			return "output " + reason
		}
	}
	return ""
}

func (r *router) remoteURL(raw string, output bool) string {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("URL %q is invalid", raw)
	}
	switch u.Scheme {
	case "s3":
		if u.Host != r.bucket {
			return fmt.Sprintf("in bucket %q, not Conquest's bucket", u.Host)
		}
		return ""
	case "http", "https":
		if output {
			return fmt.Sprintf("URL scheme %q", u.Scheme)
		}
		return ""
	default:
		return fmt.Sprintf("URL scheme %q", u.Scheme)
	}
}

func (r *router) matchRule(task *domain.Task, req *taskRequest, run runInfo) *Rule {
	name := req.Name
	if name == "" {
		name = task.EngineTaskName
	}
	for i := range r.cfg.Routing.Rules {
		rule := &r.cfg.Routing.Rules[i]
		m := rule.Match
		if len(m.Engines) > 0 && !slices.Contains(m.Engines, run.Engine) {
			continue
		}
		if len(m.TaskNames) > 0 && !slices.ContainsFunc(m.TaskNames, func(p string) bool {
			ok, _ := path.Match(p, name)
			return ok
		}) {
			continue
		}
		tagsMatch := true
		for k, v := range m.RunTags {
			if run.Tags[k] != v {
				tagsMatch = false
				break
			}
		}
		if !tagsMatch {
			continue
		}
		return rule
	}
	return nil
}

// ineligible returns why an endpoint can't take the task, or "".
func (r *router) ineligible(name, dataClass string, req *taskRequest, payload json.RawMessage) string {
	ep := r.endpoints[name]
	if ep == nil {
		return "unknown endpoint"
	}
	if !slices.Contains(ep.DataClasses, dataClass) {
		return fmt.Sprintf("data class %q not allowed", dataClass)
	}
	if req.Resources != nil {
		if ep.MaxCPUCores > 0 && req.Resources.CPUCores != nil && *req.Resources.CPUCores > ep.MaxCPUCores {
			return fmt.Sprintf("needs %d CPU cores, endpoint allows %d", *req.Resources.CPUCores, ep.MaxCPUCores)
		}
		if ep.MaxRAMGb > 0 && req.Resources.RAMGb != nil && *req.Resources.RAMGb > ep.MaxRAMGb {
			return fmt.Sprintf("needs %g GB RAM, endpoint allows %g", *req.Resources.RAMGb, ep.MaxRAMGb)
		}
	}
	if found := remote.Incompatibilities(payload, ep.TESVersion); len(found) > 0 {
		return fmt.Sprintf("TES %s endpoint can't express: %s", ep.TESVersion, strings.Join(found, ", "))
	}
	return ""
}

func parseHint(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" && !slices.Contains(out, part) {
			out = append(out, part)
		}
	}
	return out
}
