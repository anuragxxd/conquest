package routing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anuragxxd/conquest/internal/backend/remote"
	"github.com/anuragxxd/conquest/internal/domain"
)

func testConfig(mutate func(*Config)) *Config {
	cfg := &Config{
		Endpoints: []remote.EndpointConfig{
			{Name: "funnel-a", URL: "https://funnel.example.org/v1", DataClasses: []string{"public", DefaultDataClass}, ClientSelectable: true},
			{Name: "tesk-b", URL: "https://tesk.example.org/ga4gh/tes/v1", DataClasses: []string{"public"}, MaxCPUCores: 4, MaxRAMGb: 8},
			{Name: "closed", URL: "https://closed.example.org/v1"}, // no data classes
		},
		Routing: RoutingConfig{ClientHints: true},
	}
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func taskWith(payload string) *domain.Task {
	return &domain.Task{TaskID: "t1", RunID: "r1", TaskRequestPayload: json.RawMessage(payload)}
}

const nfTask = `{
	"name": "NFCORE_METHYLSEQ:METHYLSEQ:FASTQC (sample1)",
	"inputs": [{"url": "s3://conquest/v1/workflow-runs/r1/work/a/.command.sh", "path": "/work/.command.sh"},
	           {"url": "https://example.org/genome.fa", "path": "/work/genome.fa"},
	           {"content": "x", "path": "/work/x"}],
	"outputs": [{"url": "s3://conquest/v1/workflow-runs/r1/work/a/.exitcode", "path": "/work/.exitcode"}],
	"resources": {"cpu_cores": 2, "ram_gb": 6}
}`

func decide(t *testing.T, cfg *Config, task *domain.Task, run runInfo) *Record {
	t.Helper()
	rec, err := newRouter(cfg, "conquest").decide(task, run, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	return rec
}

func TestNoRuleNoHintRunsLocally(t *testing.T) {
	rec := decide(t, testConfig(nil), taskWith(nfTask), runInfo{Engine: "nextflow"})

	if !slices.Equal(rec.Candidates, []string{Local}) || rec.Rule != "" {
		t.Fatalf("record = %+v", rec)
	}
	if rec.DataClass != DefaultDataClass {
		t.Errorf("data class = %q", rec.DataClass)
	}
}

func TestRuleMatchesEngineTaskNameAndRunTags(t *testing.T) {
	cfg := testConfig(func(c *Config) {
		c.Routing.Rules = []Rule{
			{Name: "wdl-only", Match: Match{Engines: []string{"wdl"}}, Backends: []string{"funnel-a"}},
			{Name: "other-process", Match: Match{TaskNames: []string{"*:TRIMGALORE*"}}, Backends: []string{"funnel-a"}},
			{Name: "needs-tag", Match: Match{RunTags: map[string]string{"site": "x"}}, Backends: []string{"funnel-a"}},
			{Name: "fastqc", Match: Match{Engines: []string{"nextflow"}, TaskNames: []string{"*:FASTQC (*"}}, Backends: []string{"tesk-b", "funnel-a"}},
		}
	})
	run := runInfo{Engine: "nextflow", Tags: map[string]string{DataClassTag: "public"}}

	rec := decide(t, cfg, taskWith(nfTask), run)

	if rec.Rule != "fastqc" || !slices.Equal(rec.Candidates, []string{"tesk-b", "funnel-a", Local}) {
		t.Fatalf("record = %+v", rec)
	}
}

func TestNoLocalFallback(t *testing.T) {
	cfg := testConfig(func(c *Config) {
		c.Routing.Rules = []Rule{{Name: "remote-only", Backends: []string{"funnel-a"}, NoLocalFallback: true}}
	})

	rec := decide(t, cfg, taskWith(nfTask), runInfo{})
	if !slices.Equal(rec.Candidates, []string{"funnel-a"}) {
		t.Fatalf("candidates = %v", rec.Candidates)
	}

	// an excluded endpoint and no fallback leave nothing
	run := runInfo{Tags: map[string]string{DataClassTag: "restricted"}}
	if _, err := newRouter(cfg, "conquest").decide(taskWith(nfTask), run, time.Now()); err == nil {
		t.Fatal("want an error when no backend is left")
	}
}

func TestEligibility(t *testing.T) {
	cfg := testConfig(func(c *Config) {
		c.Routing.Rules = []Rule{{Name: "all", Backends: []string{"closed", "tesk-b", "funnel-a"}}}
	})
	run := runInfo{Tags: map[string]string{DataClassTag: "public"}}

	big := strings.Replace(nfTask, `"cpu_cores": 2`, `"cpu_cores": 8`, 1)
	rec := decide(t, cfg, taskWith(big), run)

	if !slices.Equal(rec.Candidates, []string{"funnel-a", Local}) {
		t.Fatalf("candidates = %v", rec.Candidates)
	}
	reasons := map[string]string{}
	for _, e := range rec.Excluded {
		reasons[e.Backend] = e.Reason
	}
	if !strings.Contains(reasons["closed"], "data class") || !strings.Contains(reasons["tesk-b"], "CPU") {
		t.Errorf("exclusions = %v", rec.Excluded)
	}
}

func TestTasksThatMustStayLocal(t *testing.T) {
	cfg := testConfig(func(c *Config) {
		c.Routing.Rules = []Rule{{Name: "everything", Backends: []string{"funnel-a"}, NoLocalFallback: true}}
	})
	for name, tc := range map[string]struct {
		task   *domain.Task
		reason string
	}{
		"shared workspace run": {
			&domain.Task{TaskRequestPayload: json.RawMessage(nfTask), Workspace: &domain.WorkspaceBinding{}}, "shared workspace",
		},
		"shared workspace tag": {
			taskWith(`{"tags": {"conquest_workspace_scope": "shared"}}`), "shared workspace",
		},
		"input in another bucket": {
			taskWith(`{"inputs": [{"url": "s3://other/x", "path": "/x"}]}`), `bucket "other"`,
		},
		"file input": {
			taskWith(`{"inputs": [{"url": "file:///workspace/x", "path": "/x"}]}`), `scheme "file"`,
		},
		"https output": {
			taskWith(`{"outputs": [{"url": "https://example.org/x", "path": "/x"}]}`), `scheme "https"`,
		},
	} {
		rec := decide(t, cfg, tc.task, runInfo{})
		if !slices.Equal(rec.Candidates, []string{Local}) || !strings.Contains(rec.LocalOnly, tc.reason) {
			t.Errorf("%s: record = %+v, want local only (%s)", name, rec, tc.reason)
		}
	}
}

func TestHintWithoutRuleSelectsClientSelectableEndpoints(t *testing.T) {
	run := runInfo{Tags: map[string]string{HintTag: "tesk-b, funnel-a, nope"}}

	rec := decide(t, testConfig(nil), taskWith(nfTask), run)

	if !slices.Equal(rec.Candidates, []string{"funnel-a", Local}) {
		t.Fatalf("candidates = %v", rec.Candidates)
	}
	reasons := map[string]string{}
	for _, e := range rec.Excluded {
		reasons[e.Backend] = e.Reason
	}
	if reasons["tesk-b"] != "not client-selectable" || reasons["nope"] != "unknown endpoint" {
		t.Errorf("exclusions = %v", rec.Excluded)
	}
}

func TestHintNarrowsAndReordersRuleBackends(t *testing.T) {
	cfg := testConfig(func(c *Config) {
		c.Routing.Rules = []Rule{{Name: "r", Backends: []string{"funnel-a", "tesk-b"}}}
	})
	run := runInfo{Tags: map[string]string{DataClassTag: "public"}}
	task := taskWith(strings.Replace(nfTask, `"resources"`, `"tags": {"conquest.tes_backend": "tesk-b,closed"}, "resources"`, 1))

	rec := decide(t, cfg, task, run)

	if !slices.Equal(rec.Candidates, []string{"tesk-b", Local}) {
		t.Fatalf("candidates = %v", rec.Candidates)
	}
	if len(rec.Excluded) != 1 || rec.Excluded[0].Reason != "not allowed by rule r" {
		t.Errorf("exclusions = %v", rec.Excluded)
	}
}

func TestTaskHintOverridesRunHintAndLocalIsAlwaysAllowed(t *testing.T) {
	run := runInfo{Tags: map[string]string{HintTag: "funnel-a"}}
	task := taskWith(`{"tags": {"conquest.tes_backend": "local"}}`)

	rec := decide(t, testConfig(nil), task, run)

	if !slices.Equal(rec.Candidates, []string{Local}) || !slices.Equal(rec.Hint, []string{Local}) {
		t.Fatalf("record = %+v", rec)
	}
}

func TestHintsIgnoredUnlessEnabled(t *testing.T) {
	cfg := testConfig(func(c *Config) { c.Routing.ClientHints = false })
	run := runInfo{Tags: map[string]string{HintTag: "funnel-a"}}

	rec := decide(t, cfg, taskWith(nfTask), run)

	if !slices.Equal(rec.Candidates, []string{Local}) || rec.Hint != nil {
		t.Fatalf("record = %+v", rec)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	if err := os.WriteFile(good, []byte(`
endpoints:
  - name: funnel-a
    url: https://funnel.example.org/v1/
    s3_url_style: endpoint
    s3_host: s3.example.org
    data_classes: [public]
    request_timeout: 10s
routing:
  client_hints: true
  rules:
    - name: qc
      match: {engines: [nextflow], task_names: ["*:FASTQC*"]}
      backends: [funnel-a, local]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(good)

	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.GatewayID != "conquest" || cfg.Endpoints[0].URL != "https://funnel.example.org/v1" ||
		cfg.Endpoints[0].RequestTimeout.Duration != 10*time.Second || cfg.Routing.Rules[0].Match.TaskNames[0] != "*:FASTQC*" {
		t.Errorf("config = %+v", cfg)
	}

	for name, body := range map[string]string{
		"unknown field":        "endpoints: []\nroutng: {}\n",
		"unknown rule backend": "endpoints: []\nrouting: {rules: [{name: r, backends: [nope]}]}\n",
		"duplicate endpoint":   "endpoints: [{name: a, url: 'https://x'}, {name: a, url: 'https://y'}]\n",
		"bad pattern":          "endpoints: []\nrouting: {rules: [{name: r, backends: [local], match: {task_names: ['[']}}]}\n",
	} {
		file := filepath.Join(dir, "bad.yaml")
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(file); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestTES10EndpointsGetOnlyTasksTheyCanExpress(t *testing.T) {
	cfg := testConfig(func(c *Config) {
		c.Endpoints[1].TESVersion = remote.TES10 // tesk-b
		c.Routing.Rules = []Rule{{Name: "r", Backends: []string{"tesk-b", "funnel-a"}}}
	})
	run := runInfo{Tags: map[string]string{DataClassTag: "public"}}
	glob := strings.Replace(nfTask, `"outputs": [`, `"outputs": [{"url": "s3://conquest/v1/workflow-runs/r1/work/a", "path": "/work/*.html", "path_prefix": "/work"}, `, 1)

	rec := decide(t, cfg, taskWith(glob), run)
	if !slices.Equal(rec.Candidates, []string{"funnel-a", Local}) ||
		len(rec.Excluded) != 1 || !strings.Contains(rec.Excluded[0].Reason, "wildcard outputs") {
		t.Fatalf("glob task: record = %+v", rec)
	}

	rec = decide(t, cfg, taskWith(nfTask), run)
	if !slices.Equal(rec.Candidates, []string{"tesk-b", "funnel-a", Local}) {
		t.Fatalf("plain task: candidates = %v", rec.Candidates)
	}
}
