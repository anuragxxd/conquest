package storage

import (
	"strings"
	"testing"
)

const testBucket = "pa-workflows"
const testRunID = "019553e8-7c3a-7abc-8def-0123456789ab"
const testTaskID = "019553e8-9f00-7bbb-9aaa-abcdef012345"

func testLayout() Layout {
	return Layout{Bucket: testBucket}
}

// ---------------------------------------------------------------------------
// Run root
// ---------------------------------------------------------------------------

func TestRunRoot(t *testing.T) {
	l := testLayout()
	got := l.RunRoot(testRunID)
	want := "v1/workflow-runs/" + testRunID + "/"
	if got != want {
		t.Errorf("RunRoot = %q, want %q", got, want)
	}
}

func TestRunRootURI(t *testing.T) {
	l := testLayout()
	got := l.RunRootURI(testRunID)
	want := "s3://pa-workflows/v1/workflow-runs/" + testRunID + "/"
	if got != want {
		t.Errorf("RunRootURI = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Request artifacts
// ---------------------------------------------------------------------------

func TestRunRequest(t *testing.T) {
	l := testLayout()
	got := l.RunRequest(testRunID)
	assertSuffix(t, got, "request/run-request.json")
	assertPrefix(t, got, "v1/workflow-runs/"+testRunID+"/")
}

func TestResolvedWorkflow(t *testing.T) {
	l := testLayout()
	got := l.ResolvedWorkflow(testRunID)
	assertSuffix(t, got, "request/resolved-workflow.json")
}

func TestResolvedInputs(t *testing.T) {
	l := testLayout()
	got := l.ResolvedInputs(testRunID)
	assertSuffix(t, got, "request/resolved-inputs.json")
}

// ---------------------------------------------------------------------------
// Workflow bundle
// ---------------------------------------------------------------------------

func TestWorkflowBundleDir(t *testing.T) {
	l := testLayout()
	got := l.WorkflowBundleDir(testRunID)
	assertSuffix(t, got, "workflow/bundle/")
}

func TestWorkflowBundleDirURI(t *testing.T) {
	l := testLayout()
	got := l.WorkflowBundleDirURI(testRunID)
	if !strings.HasSuffix(got, "workflow/bundle/") {
		t.Errorf("URI should end with workflow/bundle/, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Engine work directories
// ---------------------------------------------------------------------------

func TestNextflowWorkDir(t *testing.T) {
	l := testLayout()
	got := l.NextflowWorkDir(testRunID)
	assertSuffix(t, got, "work/nextflow/")
}

func TestNextflowWorkDirURI(t *testing.T) {
	l := testLayout()
	got := l.NextflowWorkDirURI(testRunID)
	if !strings.HasPrefix(got, "s3://pa-workflows/") {
		t.Errorf("URI should start with s3://pa-workflows/, got %q", got)
	}
	if !strings.HasSuffix(got, "work/nextflow/") {
		t.Errorf("URI should end with work/nextflow/, got %q", got)
	}
}

func TestSnakemakeWorkDir(t *testing.T) {
	l := testLayout()
	got := l.SnakemakeWorkDir(testRunID)
	assertSuffix(t, got, "work/snakemake/")
}

func TestSnakemakeWorkDirURI(t *testing.T) {
	l := testLayout()
	got := l.SnakemakeWorkDirURI(testRunID)
	if !strings.HasSuffix(got, "work/snakemake/") {
		t.Errorf("URI should end with work/snakemake/, got %q", got)
	}
}

func TestSnakemakeWorkspaceDirURI(t *testing.T) {
	l := testLayout()
	got := l.SnakemakeWorkspaceDirURI(testRunID)
	if !strings.HasSuffix(got, "work/snakemake/workspace/") {
		t.Errorf("URI should end with work/snakemake/workspace/, got %q", got)
	}
}

func TestSnakemakeTaskDirURI(t *testing.T) {
	l := testLayout()
	got := l.SnakemakeTaskDirURI(testRunID, testTaskID)
	if !strings.Contains(got, "work/snakemake/tasks/"+testTaskID+"/") {
		t.Errorf("URI should contain task path, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

func TestRunLog(t *testing.T) {
	l := testLayout()
	got := l.RunLog(testRunID)
	assertSuffix(t, got, "logs/run.log")
}

func TestRunLogURI(t *testing.T) {
	l := testLayout()
	got := l.RunLogURI(testRunID)
	if !strings.HasPrefix(got, "s3://") {
		t.Errorf("expected s3:// URI, got %q", got)
	}
	assertContains(t, got, "logs/run.log")
}

func TestTaskLog(t *testing.T) {
	l := testLayout()
	got := l.TaskLog(testRunID, testTaskID, 0, 0)
	assertContains(t, got, "logs/tasks/"+testTaskID+"/attempt-000/executor-0.log")
}

func TestTaskLog_HigherAttempt(t *testing.T) {
	l := testLayout()
	got := l.TaskLog(testRunID, testTaskID, 2, 1)
	assertContains(t, got, "attempt-002/executor-1.log")
}

func TestTaskLogURI(t *testing.T) {
	l := testLayout()
	got := l.TaskLogURI(testRunID, testTaskID, 0, 0)
	if !strings.HasPrefix(got, "s3://") {
		t.Errorf("expected s3:// URI, got %q", got)
	}
}

func TestTaskLogDir(t *testing.T) {
	l := testLayout()
	got := l.TaskLogDir(testRunID, testTaskID)
	assertSuffix(t, got, "logs/tasks/"+testTaskID+"/")
}

// ---------------------------------------------------------------------------
// Outputs
// ---------------------------------------------------------------------------

func TestOutputsDir(t *testing.T) {
	l := testLayout()
	got := l.OutputsDir(testRunID)
	assertSuffix(t, got, "outputs/")
}

func TestOutputsDirURI(t *testing.T) {
	l := testLayout()
	got := l.OutputsDirURI(testRunID)
	if !strings.HasPrefix(got, "s3://") || !strings.HasSuffix(got, "outputs/") {
		t.Errorf("OutputsDirURI = %q", got)
	}
}

// ---------------------------------------------------------------------------
// Metadata
// ---------------------------------------------------------------------------

func TestRunMetadata(t *testing.T) {
	l := testLayout()
	got := l.RunMetadata(testRunID)
	assertSuffix(t, got, "metadata/run.json")
}

func TestTaskMetadata(t *testing.T) {
	l := testLayout()
	got := l.TaskMetadata(testRunID, testTaskID)
	assertContains(t, got, "metadata/tasks/"+testTaskID+".json")
}

// ---------------------------------------------------------------------------
// Safety / edge cases
// ---------------------------------------------------------------------------

func TestNoDoubleSlashes(t *testing.T) {
	l := testLayout()
	paths := []string{
		l.RunRoot(testRunID),
		l.RunRequest(testRunID),
		l.ResolvedWorkflow(testRunID),
		l.ResolvedInputs(testRunID),
		l.WorkflowBundleDir(testRunID),
		l.NextflowWorkDir(testRunID),
		l.SnakemakeWorkDir(testRunID),
		l.RunLog(testRunID),
		l.TaskLog(testRunID, testTaskID, 0, 0),
		l.TaskLogDir(testRunID, testTaskID),
		l.OutputsDir(testRunID),
		l.RunMetadata(testRunID),
		l.TaskMetadata(testRunID, testTaskID),
	}
	for _, p := range paths {
		if strings.Contains(p, "//") {
			t.Errorf("double slash in key: %q", p)
		}
	}

	uris := []string{
		l.RunRootURI(testRunID),
		l.NextflowWorkDirURI(testRunID),
		l.SnakemakeWorkDirURI(testRunID),
		l.RunLogURI(testRunID),
		l.TaskLogURI(testRunID, testTaskID, 0, 0),
		l.OutputsDirURI(testRunID),
	}
	for _, u := range uris {
		// "s3://" is expected, but no other double slashes.
		after := strings.TrimPrefix(u, "s3://")
		if strings.Contains(after, "//") {
			t.Errorf("double slash in URI (after s3://): %q", u)
		}
	}
}

func TestSanitize_PathTraversal(t *testing.T) {
	l := testLayout()
	malicious := "../../../etc/passwd"
	got := l.RunRoot(malicious)
	if strings.Contains(got, "..") {
		t.Errorf("path traversal not sanitized: %q", got)
	}
	if strings.Contains(got, "/etc/passwd") {
		t.Errorf("path traversal reached /etc/passwd: %q", got)
	}
}

func TestSanitize_Slashes(t *testing.T) {
	l := testLayout()
	got := l.RunRoot("foo/bar/baz")
	if strings.Contains(got, "foo/bar") {
		t.Errorf("slashes not sanitized: %q", got)
	}
}

func TestDifferentBuckets(t *testing.T) {
	l1 := Layout{Bucket: "bucket-a"}
	l2 := Layout{Bucket: "bucket-b"}
	u1 := l1.RunRootURI(testRunID)
	u2 := l2.RunRootURI(testRunID)
	if !strings.HasPrefix(u1, "s3://bucket-a/") {
		t.Errorf("wrong bucket: %q", u1)
	}
	if !strings.HasPrefix(u2, "s3://bucket-b/") {
		t.Errorf("wrong bucket: %q", u2)
	}
}

func TestLayoutVersion(t *testing.T) {
	if LayoutVersion != "v1" {
		t.Errorf("LayoutVersion = %q, want v1", LayoutVersion)
	}
	l := testLayout()
	root := l.RunRoot(testRunID)
	if !strings.HasPrefix(root, "v1/") {
		t.Errorf("RunRoot should start with v1/, got %q", root)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertPrefix(t *testing.T, got, prefix string) {
	t.Helper()
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("%q should start with %q", got, prefix)
	}
}

func assertSuffix(t *testing.T, got, suffix string) {
	t.Helper()
	if !strings.HasSuffix(got, suffix) {
		t.Errorf("%q should end with %q", got, suffix)
	}
}

func assertContains(t *testing.T, got, substr string) {
	t.Helper()
	if !strings.Contains(got, substr) {
		t.Errorf("%q should contain %q", got, substr)
	}
}
