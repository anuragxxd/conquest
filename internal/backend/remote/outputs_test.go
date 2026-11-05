package remote

import (
	"path"
	"slices"
	"testing"
)

func TestExpandBraces(t *testing.T) {
	for in, want := range map[string][]string{
		"/work/*.html":                    {"/work/*.html"},
		"/work/*report.{html,txt}":        {"/work/*report.html", "/work/*report.txt"},
		"/work/*unpaired{,_1,_2}.fq.gz":   {"/work/*unpaired.fq.gz", "/work/*unpaired_1.fq.gz", "/work/*unpaired_2.fq.gz"},
		"/w/*{3prime,trimmed}{,_1}.fq.gz": {"/w/*3prime.fq.gz", "/w/*3prime_1.fq.gz", "/w/*trimmed.fq.gz", "/w/*trimmed_1.fq.gz"},
		"/w/{a,{b,c}d}.txt":               {"/w/a.txt", "/w/bd.txt", "/w/cd.txt"},
		"/w/{x}{a,b}":                     {"/w/{x}a", "/w/{x}b"},
		"/w/unbalanced{a,b":               {"/w/unbalanced{a,b"},
		"/w/{a,a}":                        {"/w/a"},
	} {
		if got := expandBraces(in); !slices.Equal(got, want) {
			t.Errorf("expandBraces(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOptionalFileOutputMatchesOnlyItsFile(t *testing.T) {
	out := optionalFileOutput(map[string]any{
		"path": "/work/x.bam.bai",
		"url":  "s3://conquest/run/work/ab/x.bam.bai",
	})

	if out["path"] != "/work/x.bam.ba[*i]" || out["path_prefix"] != "/work" || out["url"] != "s3://conquest/run/work/ab/" {
		t.Fatalf("output = %v", out)
	}
	pattern := out["path"].(string)
	for name, want := range map[string]bool{
		"/work/x.bam.bai":   true,
		"/work/x.bam.ba":    false,
		"/work/x.bam.baii":  false,
		"/work/x.bam.bai.1": false,
		"/work/x.bam.bax":   false,
	} {
		if ok, _ := path.Match(pattern, name); ok != want {
			t.Errorf("%q matches %q: %v, want %v", pattern, name, ok, want)
		}
	}
}

func TestOptionalFileOutputKeepsOutputsItCantRewrite(t *testing.T) {
	for _, out := range []map[string]any{
		{"path": "/work/x.txt", "url": "s3://conquest/run/other-name.txt"}, // object named differently
		{"path": "x.txt", "url": "s3://conquest/run/x.txt"},                // no directory
		{"path": "/work/x[1].txt", "url": "s3://conquest/run/x[1].txt"},    // glob characters
	} {
		if got := optionalFileOutput(out); got["path"] != out["path"] || got["path_prefix"] != nil {
			t.Errorf("rewrote %v to %v", out, got)
		}
	}
}

func TestOptionalFileOutputEscapedURL(t *testing.T) {
	out := optionalFileOutput(map[string]any{
		"path": "/work/out file.txt",
		"url":  "s3://conquest/run/out%20file.txt",
	})

	if out["url"] != "s3://conquest/run/" {
		t.Fatalf("url = %v", out["url"])
	}
}

func TestRewriteOutputs(t *testing.T) {
	task := map[string]any{"outputs": []any{
		map[string]any{"path": "/work/*report.{html,txt}", "path_prefix": "/work", "url": "s3://b/d"},
		map[string]any{"path": "/work/a.txt", "url": "s3://b/d/a.txt"},
		map[string]any{"path": "/work/dir", "url": "s3://b/d/dir", "type": "DIRECTORY"},
	}}

	rewriteOutputs(task, &EndpointConfig{OptionalFileOutputs: true})

	var paths []string
	for _, o := range task["outputs"].([]any) {
		paths = append(paths, o.(map[string]any)["path"].(string))
	}
	want := []string{"/work/*report.html", "/work/*report.txt", "/work/a.tx[*t]", "/work/dir"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}

	plain := map[string]any{"outputs": []any{map[string]any{"path": "/work/a.txt", "url": "s3://b/d/a.txt"}}}
	rewriteOutputs(plain, &EndpointConfig{})
	if p := plain["outputs"].([]any)[0].(map[string]any)["path"]; p != "/work/a.txt" {
		t.Errorf("without optional_file_outputs: path = %v", p)
	}
}

func TestNextflowControlFilesAreRequested(t *testing.T) {
	task := map[string]any{"outputs": []any{
		map[string]any{"path": "/work/.command.out", "url": "s3://b/run/work/ab/.command.out"},
		map[string]any{"path": "/work/.exitcode", "url": "s3://b/run/work/ab/.exitcode"},
	}}

	rewriteOutputs(task, &EndpointConfig{TESVersion: TES11})

	outs := task["outputs"].([]any)
	last := outs[len(outs)-1].(map[string]any)
	if len(outs) != 3 || last["path"] != "/work/.command.*" || last["path_prefix"] != "/work" || last["url"] != "s3://b/run/work/ab" {
		t.Fatalf("outputs = %v", outs)
	}
	if ok, _ := path.Match(last["path"].(string), "/work/.command.env"); !ok {
		t.Error("pattern doesn't match .command.env")
	}

	tes10 := map[string]any{"outputs": []any{map[string]any{"path": "/work/.exitcode", "url": "s3://b/x/.exitcode"}}}
	rewriteOutputs(tes10, &EndpointConfig{TESVersion: TES10})
	if len(tes10["outputs"].([]any)) != 1 {
		t.Error("wildcard added for a TES 1.0 endpoint")
	}

	other := map[string]any{"outputs": []any{map[string]any{"path": "/out/result.txt", "url": "s3://b/result.txt"}}}
	rewriteOutputs(other, &EndpointConfig{TESVersion: TES11})
	if len(other["outputs"].([]any)) != 1 {
		t.Error("wildcard added for a task without .exitcode")
	}
}
