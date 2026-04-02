// Package source implements workflow source validation and normalization.
//
// Batch D scope: public GitHub HTTPS repository URLs only.
// See TECHDEBT.md TD-014 for the broader adapter/resolver design debt.
package source

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Supported workflow source types.
const (
	TypeGitHubHTTPS = "github-https"
)

// ResolvedSource is the normalized result of parsing a workflow_url.
type ResolvedSource struct {
	Type     string // e.g. "github-https"
	RepoURL  string // normalized HTTPS URL without .git suffix
	Revision string // commit SHA, tag, or branch
	Ref      string // "<RepoURL>@<Revision>" for storage
}

// NextflowEngineParams holds the Nextflow-specific subset of
// workflow_engine_parameters that Batch D supports.
type NextflowEngineParams struct {
	Revision   string   `json:"revision"`              // git commit SHA, tag, or branch — required in Batch D
	Entry      string   `json:"entry,omitempty"`       // -entry workflow name
	MainScript string   `json:"main_script,omitempty"` // -main-script file
	Profiles   []string `json:"profiles,omitempty"`    // -profile values from the workflow's own nextflow.config
}

type SnakemakeEngineParams struct {
	Revision  string   `json:"revision"`
	Snakefile string   `json:"snakefile,omitempty"`
	Directory string   `json:"directory,omitempty"`
	Targets   []string `json:"targets,omitempty"`
	Jobs      int      `json:"jobs,omitempty"`
}

type WDLEngineParams struct {
	Revision         string `json:"revision"`
	MainWorkflowPath string `json:"main_workflow_path"`
}

// hexSHA40 matches a 40-char lowercase hex string (full git commit SHA).
var hexSHA40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateGitHubURL checks that a workflow_url is a public GitHub HTTPS
// repository root URL. It rejects SSH, blob/tree/archive URLs, and
// non-GitHub hosts.
//
// Accepted forms:
//   - https://github.com/<org>/<repo>
//   - https://github.com/<org>/<repo>.git
//
// Returns the normalized repo URL (without .git suffix).
func ValidateGitHubURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("workflow_url is required")
	}

	// Reject obvious non-HTTPS schemes.
	if strings.HasPrefix(rawURL, "git@") || strings.HasPrefix(rawURL, "ssh://") {
		return "", fmt.Errorf("SSH git URLs are not supported in Batch D; use https://github.com/<org>/<repo>")
	}
	if strings.HasPrefix(rawURL, "s3://") {
		return "", fmt.Errorf("s3:// workflow URLs are not supported in Batch D; use https://github.com/<org>/<repo>")
	}
	if strings.HasPrefix(rawURL, "trs://") {
		return "", fmt.Errorf("TRS workflow URLs are not supported in Batch D; use https://github.com/<org>/<repo>")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid workflow_url: %w", err)
	}

	if u.Scheme != "https" {
		return "", fmt.Errorf("workflow_url must use https:// scheme (got %q)", u.Scheme)
	}

	if u.Host != "github.com" {
		return "", fmt.Errorf("only github.com is supported in Batch D (got %q)", u.Host)
	}

	// Strip .git suffix and trailing slash for normalization.
	path := strings.TrimSuffix(u.Path, ".git")
	path = strings.TrimSuffix(path, "/")

	// Must be exactly /<org>/<repo>.
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("workflow_url must be a repository root: https://github.com/<org>/<repo> (got path %q)", u.Path)
	}

	// Reject blob/tree/archive sub-paths.
	for _, p := range parts {
		if p == "blob" || p == "tree" || p == "archive" || p == "raw" {
			return "", fmt.Errorf("workflow_url must be a repository root, not a file/tree/archive URL")
		}
	}

	// Reject query/fragment.
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("workflow_url must not contain query or fragment")
	}

	normalized := fmt.Sprintf("https://github.com/%s/%s", parts[0], parts[1])
	return normalized, nil
}

// ValidateRevision checks that a revision string is a valid 40-char hex
// commit SHA. In Batch D we require full SHAs for reproducibility.
// Tags and branches are rejected.
func ValidateRevision(rev string) error {
	if rev == "" {
		return fmt.Errorf("workflow_engine_parameters.revision is required")
	}
	if !hexSHA40.MatchString(rev) {
		return fmt.Errorf("revision must be a full 40-character commit SHA (got %q); tags and branches are not supported in Batch D", rev)
	}
	return nil
}

// NormalizeProfiles trims and validates Nextflow profile names while preserving
// user-specified order.
func NormalizeProfiles(profiles []string) ([]string, error) {
	if len(profiles) == 0 {
		return nil, nil
	}

	normalized := make([]string, 0, len(profiles))
	for i, profile := range profiles {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return nil, fmt.Errorf("profiles[%d] must not be empty", i)
		}
		if strings.Contains(profile, ",") {
			return nil, fmt.Errorf("profiles[%d] must not contain commas (got %q)", i, profile)
		}
		normalized = append(normalized, profile)
	}

	return normalized, nil
}

// Resolve validates and normalizes a workflow_url and engine parameters
// into a ResolvedSource. This is the main entry point for Batch D source
// handling.
func Resolve(workflowURL string, params *NextflowEngineParams) (*ResolvedSource, error) {
	if params == nil {
		return nil, fmt.Errorf("workflow_engine_parameters is required")
	}
	return ResolveRepository(workflowURL, params.Revision)
}

func ResolveRepository(workflowURL, revision string) (*ResolvedSource, error) {
	repoURL, err := ValidateGitHubURL(workflowURL)
	if err != nil {
		return nil, err
	}

	if err := ValidateRevision(revision); err != nil {
		return nil, err
	}

	return &ResolvedSource{
		Type:     TypeGitHubHTTPS,
		RepoURL:  repoURL,
		Revision: revision,
		Ref:      fmt.Sprintf("%s@%s", repoURL, revision),
	}, nil
}
