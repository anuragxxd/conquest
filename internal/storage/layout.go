package storage

import (
	"fmt"
	"path"
	"strings"
)

// LayoutVersion is the default version of the storage layout. Changing it
// means new runs get a different path structure; old runs remain readable
// as long as their persisted run_root_uri is used.
const LayoutVersion = "v1"

// Layout builds S3 keys and full URIs for all objects within a run.
//
// Every path method returns a bare key (no leading "s3://bucket/"). Use
// the URI methods to get full s3:// URIs for passing to Nextflow, TES
// inputs/outputs, or any code that needs a complete URL.
//
// The layout is designed around these principles:
//   - Version prefix (v1/) so the layout can evolve without migration.
//   - Run-scoped isolation: everything for a run is under one prefix.
//     Deleting the run prefix cleans up all artifacts.
//   - Engine-managed zones (work/nextflow/, work/snakemake/) are owned
//     entirely by the workflow engine. The platform writes nothing there.
//   - Platform-managed zones (request/, logs/, outputs/, metadata/) are
//     written by the platform.
//   - Attempt-aware log paths support retry semantics.
type Layout struct {
	Bucket  string // e.g. "my-workflow-bucket"
	Version string
}

// ---------------------------------------------------------------------------
// Run root
// ---------------------------------------------------------------------------

// RunRoot returns the key prefix for a run. All objects for this run live
// under this prefix.
//
//	v1/workflow-runs/<runID>/
func (l Layout) RunRoot(runID string) string {
	return fmt.Sprintf("%s/workflow-runs/%s/", l.layoutVersion(), sanitize(runID))
}

func (l Layout) CurrentVersion() string {
	return l.layoutVersion()
}

// RunRootURI returns the full S3 URI for the run root.
//
//	s3://<bucket>/v1/workflow-runs/<runID>/
func (l Layout) RunRootURI(runID string) string {
	return l.uri(l.RunRoot(runID))
}

// ---------------------------------------------------------------------------
// Request artifacts
// ---------------------------------------------------------------------------

// RunRequest returns the key for the original WES RunRequest JSON.
func (l Layout) RunRequest(runID string) string {
	return l.runPath(runID, "request/run-request.json")
}

// ResolvedWorkflow returns the key for the resolved workflow reference.
func (l Layout) ResolvedWorkflow(runID string) string {
	return l.runPath(runID, "request/resolved-workflow.json")
}

// ResolvedInputs returns the key for resolved input metadata.
func (l Layout) ResolvedInputs(runID string) string {
	return l.runPath(runID, "request/resolved-inputs.json")
}

// ---------------------------------------------------------------------------
// Workflow bundle
// ---------------------------------------------------------------------------

// WorkflowBundleDir returns the key prefix where workflow files are stored
// (from TRS resolution or direct upload).
func (l Layout) WorkflowBundleDir(runID string) string {
	return l.runPath(runID, "workflow/bundle/")
}

func (l Layout) WorkflowBundleDirURI(runID string) string {
	return l.uri(l.WorkflowBundleDir(runID))
}

// ---------------------------------------------------------------------------
// Engine work directories
//
// These prefixes are handed to the workflow engine as its work directory.
// The engine owns everything underneath — the platform never writes here.
// ---------------------------------------------------------------------------

// NextflowWorkDir returns the key prefix for Nextflow's work tree.
// Pass this (as a full URI) to Nextflow's -work-dir argument.
func (l Layout) NextflowWorkDir(runID string) string {
	return l.runPath(runID, "work/nextflow/")
}

// NextflowWorkDirURI returns the full S3 URI for the Nextflow work dir.
//
//	s3://<bucket>/v1/workflow-runs/<runID>/work/nextflow/
func (l Layout) NextflowWorkDirURI(runID string) string {
	return l.uri(l.NextflowWorkDir(runID))
}

// SnakemakeWorkDir returns the key prefix for Snakemake's work tree.
func (l Layout) SnakemakeWorkDir(runID string) string {
	return l.runPath(runID, "work/snakemake/")
}

// SnakemakeWorkDirURI returns the full S3 URI for the Snakemake work dir.
func (l Layout) SnakemakeWorkDirURI(runID string) string {
	return l.uri(l.SnakemakeWorkDir(runID))
}

func (l Layout) SnakemakeWorkspaceDir(runID string) string {
	return l.runPath(runID, "work/snakemake/workspace/")
}

func (l Layout) SnakemakeWorkspaceDirURI(runID string) string {
	return l.uri(l.SnakemakeWorkspaceDir(runID))
}

func (l Layout) SnakemakeTaskDir(runID, taskID string) string {
	return l.runPath(runID, fmt.Sprintf("work/snakemake/tasks/%s/", sanitize(taskID)))
}

func (l Layout) SnakemakeTaskDirURI(runID, taskID string) string {
	return l.uri(l.SnakemakeTaskDir(runID, taskID))
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

// RunLog returns the key for the overall run log.
func (l Layout) RunLog(runID string) string {
	return l.runPath(runID, "logs/run.log")
}

// RunLogURI returns the full S3 URI for the run log.
func (l Layout) RunLogURI(runID string) string {
	return l.uri(l.RunLog(runID))
}

// TaskLog returns the key for a specific task's log, scoped by attempt
// and executor index. This supports the TES convention where the logs
// array grows on retry, and each task may have multiple executors.
//
//	logs/tasks/<taskID>/attempt-<NNN>/executor-<N>.log
func (l Layout) TaskLog(runID, taskID string, attempt int, executor int) string {
	return l.runPath(runID, fmt.Sprintf(
		"logs/tasks/%s/attempt-%03d/executor-%d.log",
		sanitize(taskID), attempt, executor,
	))
}

// TaskLogURI returns the full S3 URI for a task log.
func (l Layout) TaskLogURI(runID, taskID string, attempt int, executor int) string {
	return l.uri(l.TaskLog(runID, taskID, attempt, executor))
}

// TaskLogDir returns the key prefix for all logs of a given task.
func (l Layout) TaskLogDir(runID, taskID string) string {
	return l.runPath(runID, fmt.Sprintf("logs/tasks/%s/", sanitize(taskID)))
}

// ---------------------------------------------------------------------------
// Outputs
// ---------------------------------------------------------------------------

// OutputsDir returns the key prefix for final workflow outputs.
func (l Layout) OutputsDir(runID string) string {
	return l.runPath(runID, "outputs/")
}

// OutputsDirURI returns the full S3 URI for the outputs directory.
func (l Layout) OutputsDirURI(runID string) string {
	return l.uri(l.OutputsDir(runID))
}

// WorkflowOutputs returns the key for the persisted final workflow outputs JSON.
func (l Layout) WorkflowOutputs(runID string) string {
	return l.runPath(runID, "metadata/workflow-outputs.json")
}

// WorkflowOutputsURI returns the full S3 URI for the final workflow outputs JSON.
func (l Layout) WorkflowOutputsURI(runID string) string {
	return l.uri(l.WorkflowOutputs(runID))
}

// CromwellMetadata returns the key for the persisted Cromwell metadata JSON.
func (l Layout) CromwellMetadata(runID string) string {
	return l.runPath(runID, "metadata/cromwell-metadata.json")
}

// CromwellMetadataURI returns the full S3 URI for the Cromwell metadata JSON.
func (l Layout) CromwellMetadataURI(runID string) string {
	return l.uri(l.CromwellMetadata(runID))
}

// ---------------------------------------------------------------------------
// Metadata
// ---------------------------------------------------------------------------

// RunMetadata returns the key for a run state snapshot.
func (l Layout) RunMetadata(runID string) string {
	return l.runPath(runID, "metadata/run.json")
}

// TaskMetadata returns the key for a task state snapshot.
func (l Layout) TaskMetadata(runID, taskID string) string {
	return l.runPath(runID, fmt.Sprintf("metadata/tasks/%s.json", sanitize(taskID)))
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

func (l Layout) runPath(runID, suffix string) string {
	// path.Join strips trailing slashes, but we need them for S3 directory
	// prefixes. Preserve the trailing slash if suffix has one.
	joined := path.Join(l.RunRoot(runID), suffix)
	if strings.HasSuffix(suffix, "/") && !strings.HasSuffix(joined, "/") {
		joined += "/"
	}
	return joined
}

func (l Layout) uri(key string) string {
	// Ensure no double slashes except in "s3://".
	return fmt.Sprintf("s3://%s/%s", l.Bucket, key)
}

func (l Layout) layoutVersion() string {
	if strings.TrimSpace(l.Version) != "" {
		return l.Version
	}
	return LayoutVersion
}

// sanitize strips characters that could cause path traversal or S3 key
// problems. IDs should already be UUIDv7 (safe), but defense in depth.
func sanitize(id string) string {
	id = strings.ReplaceAll(id, "..", "")
	id = strings.ReplaceAll(id, "/", "")
	id = strings.ReplaceAll(id, "\\", "")
	return id
}
