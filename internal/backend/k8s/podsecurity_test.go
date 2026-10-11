package k8s

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	psaapi "k8s.io/pod-security-admission/api"
	psapolicy "k8s.io/pod-security-admission/policy"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
)

// restrictedPodSecurity is the restricted profile with the chart's default
// UID/GID.
var restrictedPodSecurity = PodSecurityConfig{Restricted: true, RunAsUser: 1000, RunAsGroup: 1000}

// checkRestricted evaluates a pod template with the upstream Pod Security
// Admission checks: the same code the API server runs for a namespace
// labelled pod-security.kubernetes.io/enforce=restricted.
func checkRestricted(t *testing.T, tmpl corev1.PodTemplateSpec) psapolicy.AggregateCheckResult {
	t.Helper()
	evaluator, err := psapolicy.NewEvaluator(psapolicy.DefaultChecks(), nil)
	if err != nil {
		t.Fatalf("build pod security evaluator: %v", err)
	}
	lv := psaapi.LevelVersion{Level: psaapi.LevelRestricted, Version: psaapi.LatestVersion()}
	return psapolicy.AggregateCheckResults(evaluator.EvaluatePod(lv, &tmpl.ObjectMeta, &tmpl.Spec))
}

// assertRestrictedPod fails unless the pod passes the restricted checks, runs
// as the configured UID/GID and does not mount a ServiceAccount token.
func assertRestrictedPod(t *testing.T, tmpl corev1.PodTemplateSpec) {
	t.Helper()
	if result := checkRestricted(t, tmpl); !result.Allowed {
		t.Fatalf("pod violates the restricted Pod Security Standard: %s", result.ForbiddenDetail())
	}

	sc := tmpl.Spec.SecurityContext
	if sc == nil || sc.RunAsUser == nil || sc.RunAsGroup == nil || sc.FSGroup == nil {
		t.Fatalf("pod security context missing UID/GID/fsGroup: %+v", sc)
	}
	if *sc.RunAsUser != 1000 || *sc.RunAsGroup != 1000 || *sc.FSGroup != 1000 {
		t.Fatalf("pod runs as %d:%d fsGroup %d, want 1000:1000 fsGroup 1000", *sc.RunAsUser, *sc.RunAsGroup, *sc.FSGroup)
	}
	assertNoServiceAccountToken(t, tmpl.Spec)
}

func assertNoServiceAccountToken(t *testing.T, spec corev1.PodSpec) {
	t.Helper()
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("pod must not automount the conquest ServiceAccount token")
	}
}

func TestBuildJobSpec_RestrictedPassesPodSecurityAdmission(t *testing.T) {
	cases := []struct {
		name    string
		binding *domain.WorkspaceBinding
		ws      WorkspaceStrategy
	}{
		{"emptydir", nil, emptyDirWS()},
		{"pvc-rwx", &domain.WorkspaceBinding{
			Kind:       domain.WorkspaceKindPVC,
			ClaimName:  "conquest-workspace-run-123",
			AccessMode: domain.WorkspaceAccessModeRWX,
		}, &pvcWorkspaceRWX{}},
		{"pvc-rwo", &domain.WorkspaceBinding{
			Kind:       domain.WorkspaceKindPVC,
			ClaimName:  "conquest-workspace-run-123",
			AccessMode: domain.WorkspaceAccessModeRWO,
		}, &pvcWorkspaceRWO{}},
		{"objectfs", &domain.WorkspaceBinding{
			Kind:      domain.WorkspaceKindObjectFS,
			ClaimName: "conquest-workspace-run-123",
			PVName:    "conquest-objectfs-run-123",
		}, &objectFSWorkspace{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := domain.NewTask("run-123")
			task.TESTaskID = "tes-" + tc.name
			// ubuntu runs as root by default; the pod must still start as 1000.
			task.TaskRequestPayload = json.RawMessage(`{
				"name": "test-task",
				"executors": [{"image": "ubuntu:24.04", "command": ["bash", "-c", "echo hi"], "workdir": "/work"}]
			}`)
			task.Workspace = tc.binding

			job, err := buildJobSpec(task, Config{
				Namespace:     "services",
				ConquestImage: "example.com/conquest:latest",
				S3Bucket:      "conquest",
				S3Endpoint:    "http://minio:9000",
				S3Region:      "us-east-1",
				PodSecurity:   restrictedPodSecurity,
			}, tc.ws)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertRestrictedPod(t, job.Spec.Template)
		})
	}
}

func TestBuildRunnerJobSpec_RestrictedPassesPodSecurityAdmission(t *testing.T) {
	cases := []struct {
		engine  string
		binding *domain.WorkspaceBinding
		ws      WorkspaceStrategy
	}{
		{"nextflow", nil, emptyDirWS()},
		{"snakemake", &domain.WorkspaceBinding{
			Kind:       domain.WorkspaceKindPVC,
			ClaimName:  "conquest-workspace-test",
			AccessMode: domain.WorkspaceAccessModeRWX,
		}, &pvcWorkspaceRWX{}},
		{"wdl", &domain.WorkspaceBinding{
			Kind:      domain.WorkspaceKindObjectFS,
			ClaimName: "conquest-workspace-test",
			PVName:    "conquest-objectfs-test",
		}, &objectFSWorkspace{}},
	}

	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			run := domain.NewRun("WF", "1", tc.engine, "1", nil, "", "v1", "")
			job, err := buildRunnerJobSpec(run, RunConfig{
				Namespace:      "services",
				ServiceAccount: "conquest",
				PodSecurity:    restrictedPodSecurity,
			}, &backend.RunnerSpec{
				Image:   "example.com/conquest-runner:latest",
				Command: []string{"/usr/local/bin/conquest"},
				Args:    []string{"runner", tc.engine},
			}, tc.ws, tc.binding)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertRestrictedPod(t, job.Spec.Template)
		})
	}
}

func TestBuildJobSpec_BaselineKeepsImageUser(t *testing.T) {
	task := domain.NewTask("run-123")
	task.TESTaskID = "tes-baseline"
	task.TaskRequestPayload = json.RawMessage(`{
		"name": "test-task",
		"executors": [{"image": "ubuntu:24.04", "command": ["bash", "-c", "echo hi"], "workdir": "/work"}]
	}`)

	job, err := buildJobSpec(task, Config{
		Namespace:     "services",
		ConquestImage: "example.com/conquest:latest",
		S3Bucket:      "conquest",
	}, emptyDirWS())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spec := job.Spec.Template.Spec
	if spec.SecurityContext != nil {
		t.Fatalf("baseline emptydir pod security context = %+v, want nil", spec.SecurityContext)
	}
	for _, c := range append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...) {
		if c.SecurityContext != nil {
			t.Fatalf("baseline container %q has security context %+v, want nil", c.Name, c.SecurityContext)
		}
	}
	assertNoServiceAccountToken(t, spec)

	// Guards against a vacuous check: the evaluator must reject this pod.
	if result := checkRestricted(t, job.Spec.Template); result.Allowed {
		t.Fatal("baseline pod unexpectedly passes the restricted checks")
	}
}

func TestBuildRunnerJobSpec_BaselineDoesNotMountServiceAccountToken(t *testing.T) {
	run := domain.NewRun("NFL", "DSL2", "nextflow", "24.10.4", nil, "", "v1", "")
	job, err := buildRunnerJobSpec(run, RunConfig{ServiceAccount: "conquest"}, &backend.RunnerSpec{
		Image:   "example.com/conquest-nf-24.10.4-runner:latest",
		Command: []string{"/usr/local/bin/conquest"},
		Args:    []string{"runner", "nextflow"},
	}, emptyDirWS(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNoServiceAccountToken(t, job.Spec.Template.Spec)
}
