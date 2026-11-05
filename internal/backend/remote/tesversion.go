package remote

import (
	"encoding/json"
	"strings"
)

// TES 1.1 added fields that 1.0 servers don't know, and some reject
// requests with unknown fields (TESK's API does).

// Incompatibilities lists what a stored task needs that a TES version
// can't express, or nil if the version can run the task.
func Incompatibilities(payload json.RawMessage, version string) []string {
	if version != TES10 {
		return nil
	}
	var task struct {
		Inputs []struct {
			Streamable bool `json:"streamable"`
		} `json:"inputs"`
		Outputs []struct {
			Path       string `json:"path"`
			PathPrefix string `json:"path_prefix"`
		} `json:"outputs"`
		Executors []struct {
			IgnoreError bool `json:"ignore_error"`
		} `json:"executors"`
		Resources *struct {
			BackendParameters map[string]string `json:"backend_parameters"`
		} `json:"resources"`
	}
	if json.Unmarshal(payload, &task) != nil {
		return []string{"undecodable task"}
	}
	var found []string
	add := func(what string) {
		for _, f := range found {
			if f == what {
				return
			}
		}
		found = append(found, what)
	}
	for _, o := range task.Outputs {
		if o.PathPrefix != "" || strings.ContainsAny(o.Path, "*?[") {
			add("wildcard outputs")
		}
	}
	for _, i := range task.Inputs {
		if i.Streamable {
			add("streamable inputs")
		}
	}
	for _, e := range task.Executors {
		if e.IgnoreError {
			add("ignore_error")
		}
	}
	if task.Resources != nil && len(task.Resources.BackendParameters) > 0 {
		add("backend_parameters")
	}
	return found
}

// downgradeTo10 removes the TES 1.1 fields from a task that
// Incompatibilities found expressible in TES 1.0, i.e. fields left at
// values that mean nothing.
func downgradeTo10(task map[string]any) {
	strip := func(field string, keys ...string) {
		items, _ := task[field].([]any)
		for _, item := range items {
			if obj, ok := item.(map[string]any); ok {
				for _, k := range keys {
					delete(obj, k)
				}
			}
		}
	}
	strip("inputs", "streamable")
	strip("outputs", "path_prefix")
	strip("executors", "ignore_error")
	if res, ok := task["resources"].(map[string]any); ok {
		delete(res, "backend_parameters")
		delete(res, "backend_parameters_strict")
	}
}
