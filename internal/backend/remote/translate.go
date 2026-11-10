package remote

import (
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

// translate turns a stored Conquest task request into the body sent to an
// endpoint. Fields Conquest doesn't model are kept as they are.
//
//   - Server-set fields are dropped.
//   - Conquest's own tags (conquest_*, conquest.*) are dropped; the
//     endpoint's configured tags and the Conquest task ID are added.
//   - s3:// URLs of inputs and outputs are written in the endpoint's style.
func translate(payload json.RawMessage, cfg *EndpointConfig, taskID string) ([]byte, error) {
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
			rewritten, err := s3URL(raw, cfg.S3URLStyle, cfg.S3Host)
			if err != nil {
				return nil, fmt.Errorf("%s url %q: %w", field, raw, err)
			}
			obj["url"] = rewritten
		}
	}
	rewriteOutputs(task, cfg)
	if cfg.TESVersion == TES10 {
		if found := Incompatibilities(payload, TES10); len(found) > 0 {
			return nil, fmt.Errorf("TES 1.0 can't express: %s", strings.Join(found, ", "))
		}
		downgradeTo10(task)
	}
	return json.Marshal(task)
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
