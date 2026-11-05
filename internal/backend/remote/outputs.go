package remote

import (
	"net/url"
	"path"
	"strings"
)

// Wildcard outputs (TES 1.1) leave the glob syntax to each implementation.
// Engines write their own: Nextflow's has brace alternatives such as
// `*{1,2}.fq.gz`, which Go's path.Match (Funnel) and Python's fnmatch
// don't have, so such outputs silently match nothing.

// rewriteOutputs expands brace alternatives in wildcard outputs, adds
// Nextflow's undeclared control files (TES 1.1 endpoints), and, if the
// endpoint is configured for it, sends FILE outputs as single-file
// wildcards (see EndpointConfig.OptionalFileOutputs).
func rewriteOutputs(task map[string]any, cfg *EndpointConfig) {
	items, ok := task["outputs"].([]any)
	if !ok {
		return
	}
	if cfg.TESVersion == TES11 {
		if extra := controlFilesOutput(items); extra != nil {
			items = append(items, extra)
		}
	}
	var out []any
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		p, _ := obj["path"].(string)
		switch {
		case isWildcard(obj) && strings.ContainsRune(p, '{'):
			for _, alt := range expandBraces(p) {
				clone := cloneMap(obj)
				clone["path"] = alt
				out = append(out, clone)
			}
		case cfg.OptionalFileOutputs && !isWildcard(obj) && outputType(obj) == "FILE":
			out = append(out, optionalFileOutput(obj))
		default:
			out = append(out, obj)
		}
	}
	task["outputs"] = out
}

func isWildcard(output map[string]any) bool {
	p, _ := output["path"].(string)
	prefix, _ := output["path_prefix"].(string)
	return prefix != "" || strings.ContainsAny(p, "*?[")
}

func outputType(output map[string]any) string {
	t, _ := output["type"].(string)
	if t == "" {
		return "FILE"
	}
	return t
}

// optionalFileOutput rewrites a FILE output as a wildcard matching only that
// file: the last character c of the name becomes the class [*c]. The path
// then contains `*`, which some servers (Funnel) require to treat it as a
// wildcard, but the class matches c (or a literal `*`), nothing else. The
// output URL becomes its directory and path_prefix the file's directory, so
// the uploaded object's URL is unchanged. Outputs this can't be done for
// are returned as they are.
func optionalFileOutput(output map[string]any) map[string]any {
	p, _ := output["path"].(string)
	rawURL, _ := output["url"].(string)
	dir, name := path.Split(p)
	if name == "" || dir == "" || strings.ContainsAny(name, `*?[]\`) {
		return output
	}
	u, err := url.Parse(rawURL)
	if err != nil || path.Base(u.Path) != name {
		return output // the object isn't named after the file
	}
	last := name[len(name)-1:]
	clone := cloneMap(output)
	clone["path"] = dir + name[:len(name)-1] + "[*" + last + "]"
	clone["path_prefix"] = strings.TrimSuffix(dir, "/")
	clone["url"] = strings.TrimSuffix(rawURL, url.PathEscape(name))
	if clone["url"] == rawURL {
		clone["url"] = strings.TrimSuffix(rawURL, name)
	}
	return clone
}

// expandBraces expands shell-style brace alternatives, e.g.
// "*{a,b}{,_1}.gz" → "*a.gz", "*a_1.gz", "*b.gz", "*b_1.gz". Text without
// a complete brace group with a top-level comma is returned as it is.
func expandBraces(s string) []string {
	open := -1
	depth := 0
	for i, r := range s {
		switch r {
		case '{':
			if depth == 0 {
				open = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth > 0 {
				continue
			}
			alts := splitTopLevel(s[open+1 : i])
			if len(alts) < 2 {
				// "{x}" is no alternative; expand the rest
				var out []string
				for _, rest := range expandBraces(s[i+1:]) {
					out = append(out, s[:i+1]+rest)
				}
				return out
			}
			var out []string
			for _, alt := range alts {
				out = append(out, expandBraces(s[:open]+alt+s[i+1:])...)
			}
			return dedupe(out)
		}
	}
	return []string{s}
}

func splitTopLevel(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i, r := range s {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// controlFilesOutput returns a wildcard output for all of a Nextflow task's
// control files (.command.env, .command.trace, ...), or nil if the task
// isn't one. nf-ga4gh declares only .command.out/.err/.log and .exitcode,
// but Nextflow reads others back from the work directory, e.g.
// .command.env for `env` and `eval` outputs. Conquest's own filer uploads
// every .command.* file next to .exitcode (internal/filer); this asks a
// remote endpoint to do the same.
func controlFilesOutput(outputs []any) map[string]any {
	for _, item := range outputs {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		p, _ := obj["path"].(string)
		u, _ := obj["url"].(string)
		dir, name := path.Split(p)
		if name != ".exitcode" || dir == "" || !strings.HasSuffix(u, "/.exitcode") {
			continue
		}
		dir = strings.TrimSuffix(dir, "/")
		return map[string]any{
			"path":        dir + "/.command.*",
			"path_prefix": dir,
			"url":         strings.TrimSuffix(u, "/.exitcode"),
			"type":        "FILE",
		}
	}
	return nil
}
