package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// TaskIDTag is set on every forwarded task, so operators of the remote
// endpoint can trace it back to Conquest.
const TaskIDTag = "conquest_task_id"

// serverFields are set by TES servers; a client must not send them.
var serverFields = []string{"id", "state", "logs", "creation_time"}

// Storage gives remote endpoints access to Conquest's objects without
// credentials: capability URLs of the storage gateway, and listings to
// expand directory inputs, which HTTP can't list.
type Storage interface {
	URL(taskID, bucket, key string) string
	ListPrefix(ctx context.Context, bucket, prefix string) ([]string, error)
}

// translate turns a stored Conquest task request into the body sent to an
// endpoint. Fields Conquest doesn't model are kept as they are.
//
//   - Server-set fields are dropped.
//   - Conquest's own tags (conquest_*, conquest.*) are dropped; the
//     endpoint's configured tags and the Conquest task ID are added.
//   - Outputs are adapted to what TES servers support (rewriteOutputs).
//   - With the gateway style, DIRECTORY inputs become one FILE input per
//     object.
//   - s3:// URLs of inputs and outputs are written in the endpoint's style.
func translate(ctx context.Context, payload json.RawMessage, cfg *EndpointConfig, taskID string, st Storage) ([]byte, error) {
	var task map[string]any
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, fmt.Errorf("decode stored task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("stored task is empty")
	}
	for _, f := range serverFields {
		delete(task, f)
	}

	tags := map[string]any{}
	if old, ok := task["tags"].(map[string]any); ok {
		for k, v := range old {
			if !isConquestTag(k) {
				tags[k] = v
			}
		}
	}
	for k, v := range cfg.Tags {
		tags[k] = v
	}
	tags[TaskIDTag] = taskID
	task["tags"] = tags

	rewriteOutputs(task, cfg)
	if cfg.S3URLStyle == S3StyleGateway {
		if st == nil {
			return nil, fmt.Errorf("s3_url_style %q needs the storage gateway", S3StyleGateway)
		}
		if err := expandDirectoryInputs(ctx, task, st); err != nil {
			return nil, err
		}
	}

	for _, field := range []string{"inputs", "outputs"} {
		items, _ := task[field].([]any)
		for _, item := range items {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			raw, ok := obj["url"].(string)
			if !ok || raw == "" {
				continue
			}
			rewritten, err := s3URLFor(raw, cfg, taskID, st)
			if err != nil {
				return nil, fmt.Errorf("%s url %q: %w", field, raw, err)
			}
			obj["url"] = rewritten
		}
	}
	if cfg.TESVersion == TES10 {
		if found := Incompatibilities(payload, TES10); len(found) > 0 {
			return nil, fmt.Errorf("TES 1.0 can't express: %s", strings.Join(found, ", "))
		}
		downgradeTo10(task)
	}
	return json.Marshal(task)
}

// expandDirectoryInputs replaces each s3:// DIRECTORY input by FILE inputs
// for the objects below its URL, at the same relative paths.
func expandDirectoryInputs(ctx context.Context, task map[string]any, st Storage) error {
	items, _ := task["inputs"].([]any)
	var out []any
	for _, item := range items {
		obj, ok := item.(map[string]any)
		raw, _ := obj["url"].(string)
		if !ok || obj["type"] != "DIRECTORY" || !strings.HasPrefix(raw, "s3://") {
			out = append(out, item)
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return fmt.Errorf("input url %q: not an s3://<bucket>/<key> URL", raw)
		}
		prefix := strings.TrimPrefix(u.Path, "/")
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		keys, err := st.ListPrefix(ctx, u.Host, prefix)
		if err != nil {
			return fmt.Errorf("list directory input %q: %w", raw, err)
		}
		base, _ := obj["path"].(string)
		for _, key := range keys {
			rel := strings.TrimPrefix(key, prefix)
			if rel == "" || strings.HasSuffix(rel, "/") {
				continue
			}
			file := map[string]any{
				"url":  "s3://" + u.Host + "/" + key,
				"path": strings.TrimRight(base, "/") + "/" + rel,
				"type": "FILE",
			}
			if name, ok := obj["name"]; ok {
				file["name"] = name
			}
			out = append(out, file)
		}
	}
	task["inputs"] = out
	return nil
}

func s3URLFor(raw string, cfg *EndpointConfig, taskID string, st Storage) (string, error) {
	if cfg.S3URLStyle != S3StyleGateway || !strings.HasPrefix(raw, "s3://") {
		return s3URL(raw, cfg.S3URLStyle, cfg.S3Host)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not an s3://<bucket>/<key> URL")
	}
	return st.URL(taskID, u.Host, strings.TrimPrefix(u.Path, "/")), nil
}

func isConquestTag(key string) bool {
	return strings.HasPrefix(key, "conquest_") || strings.HasPrefix(key, "conquest.")
}

// s3URL writes an s3://<bucket>/<key> URL in the given style. Other URLs
// are returned unchanged.
func s3URL(raw, style, s3Host string) (string, error) {
	if !strings.HasPrefix(raw, "s3://") || style != S3StyleEndpoint {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not an s3://<bucket>/<key> URL")
	}
	return "s3://" + s3Host + "/" + u.Host + u.EscapedPath(), nil
}
