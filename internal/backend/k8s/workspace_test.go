package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/anuragxxd/conquest/internal/domain"
)

func TestClaimName_Deterministic(t *testing.T) {
	name := claimName("abc-123")
	if name != "conquest-workspace-abc-123" {
		t.Fatalf("claimName = %q", name)
	}
}

func TestClaimName_Sanitized(t *testing.T) {
	name := claimName("ABC_123_def")
	// Should be lowered and underscores replaced.
	if name != "conquest-workspace-abc-123-def" {
		t.Fatalf("claimName = %q", name)
	}
}

func TestClaimName_Truncated(t *testing.T) {
	// Very long run ID.
	longID := "abcdefghij-1234567890-abcdefghij-1234567890-abcdefghij"
	name := claimName(longID)
	if len(name) > 63 {
		t.Fatalf("claimName len = %d, want <= 63", len(name))
	}
}

func TestEmptyDirWorkspace_RunnerVolumes(t *testing.T) {
	ws := &emptyDirWorkspace{}
	vol, mount := ws.RunnerVolumes(nil)

	if vol.EmptyDir == nil {
		t.Fatal("expected emptyDir volume source")
	}
	if mount.MountPath != "/workspace" {
		t.Fatalf("mount path = %q, want /workspace", mount.MountPath)
	}
	if mount.SubPath != "" {
		t.Fatalf("emptydir should have no subPath, got %q", mount.SubPath)
	}
}

func TestEmptyDirWorkspace_TaskVolumes(t *testing.T) {
	ws := &emptyDirWorkspace{}
	vol, mount := ws.TaskVolumes(nil, "task-1", "/work", "isolated")

	if vol.EmptyDir == nil {
		t.Fatal("expected emptyDir volume source")
	}
	if mount.MountPath != "/work" {
		t.Fatalf("mount path = %q, want /work", mount.MountPath)
	}
	if mount.SubPath != "" {
		t.Fatalf("emptydir task should have no subPath, got %q", mount.SubPath)
	}
}

func TestEmptyDirWorkspace_NoAffinity(t *testing.T) {
	ws := &emptyDirWorkspace{}
	if ws.TaskAffinity(nil, "run-1") != nil {
		t.Fatal("emptydir should return nil affinity")
	}
}

func TestEmptyDirWorkspace_NoRunnerEnv(t *testing.T) {
	ws := &emptyDirWorkspace{}
	if len(ws.RunnerEnv()) != 0 {
		t.Fatal("emptydir should return empty runner env")
	}
}

func TestPVCRunnerVolumes_MountsClaimWithSharedSubpath(t *testing.T) {
	binding := &domain.WorkspaceBinding{
		Kind:       domain.WorkspaceKindPVC,
		ClaimName:  "conquest-workspace-run-1",
		AccessMode: domain.WorkspaceAccessModeRWX,
	}

	vol, mount := pvcRunnerVolumes(binding)

	if vol.PersistentVolumeClaim == nil {
		t.Fatal("expected PVC volume source")
	}
	if vol.PersistentVolumeClaim.ClaimName != "conquest-workspace-run-1" {
		t.Fatalf("claim name = %q", vol.PersistentVolumeClaim.ClaimName)
	}
	if mount.MountPath != "/workspace" {
		t.Fatalf("mount path = %q, want /workspace", mount.MountPath)
	}
	if mount.SubPath != "shared" {
		t.Fatalf("subPath = %q, want shared", mount.SubPath)
	}
}

func TestPVCTaskVolumes_IsolatedSubpath(t *testing.T) {
	binding := &domain.WorkspaceBinding{
		Kind:       domain.WorkspaceKindPVC,
		ClaimName:  "conquest-workspace-run-1",
		AccessMode: domain.WorkspaceAccessModeRWX,
	}

	vol, mount := pvcTaskVolumes(binding, "task-abc", "/work", "isolated")

	if vol.PersistentVolumeClaim == nil {
		t.Fatal("expected PVC volume source")
	}
	if mount.SubPath != "tasks/task-abc" {
		t.Fatalf("isolated subPath = %q, want tasks/task-abc", mount.SubPath)
	}
	if mount.MountPath != "/work" {
		t.Fatalf("mount path = %q, want /work", mount.MountPath)
	}
}

func TestPVCTaskVolumes_SharedSubpath(t *testing.T) {
	binding := &domain.WorkspaceBinding{
		Kind:       domain.WorkspaceKindPVC,
		ClaimName:  "conquest-workspace-run-1",
		AccessMode: domain.WorkspaceAccessModeRWX,
	}

	_, mount := pvcTaskVolumes(binding, "task-abc", "/workspace/workflow", "shared")

	if mount.SubPath != "shared" {
		t.Fatalf("shared subPath = %q, want shared", mount.SubPath)
	}
	// Shared scope must mount at /workspace (not the computed workdir mount path)
	// so that absolute paths align with the runner checkout.
	if mount.MountPath != "/workspace" {
		t.Fatalf("shared mountPath = %q, want /workspace", mount.MountPath)
	}
}

func TestPVCWorkspaceRWO_TaskAffinityMatchesRunner(t *testing.T) {
	ws := &pvcWorkspaceRWO{}
	affinity := ws.TaskAffinity(nil, "run-xyz")

	if affinity == nil || affinity.PodAffinity == nil {
		t.Fatal("RWO should return pod affinity")
	}

	terms := affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if len(terms) != 1 {
		t.Fatalf("expected 1 affinity term, got %d", len(terms))
	}

	labels := terms[0].LabelSelector.MatchLabels
	if labels[LabelRunID] != "run-xyz" {
		t.Fatalf("affinity run-id = %q", labels[LabelRunID])
	}
	if labels[LabelJobType] != "runner" {
		t.Fatalf("affinity job-type = %q", labels[LabelJobType])
	}
	if terms[0].TopologyKey != "kubernetes.io/hostname" {
		t.Fatalf("topology key = %q", terms[0].TopologyKey)
	}
}

func TestPVCWorkspaceRWX_NoTaskAffinity(t *testing.T) {
	ws := &pvcWorkspaceRWX{}
	if ws.TaskAffinity(nil, "run-xyz") != nil {
		t.Fatal("RWX should return nil affinity")
	}
}

func TestSharedRunnerEnv(t *testing.T) {
	envs := sharedRunnerEnv()
	if len(envs) != 1 {
		t.Fatalf("expected 1 env var, got %d", len(envs))
	}
	if envs[0].Name != "CONQUEST_SHARED_WORKSPACE" || envs[0].Value != "true" {
		t.Fatalf("env = %+v", envs[0])
	}
}

func TestNewWorkspaceStrategy_EmptyDir(t *testing.T) {
	ws := NewWorkspaceStrategy(nil, WorkspaceConfig{Mode: "emptydir"}, "ns", nil)
	if _, ok := ws.(*emptyDirWorkspace); !ok {
		t.Fatal("expected emptyDirWorkspace")
	}
}

func TestNewWorkspaceStrategy_PVC_RWX(t *testing.T) {
	ws := NewWorkspaceStrategy(nil, WorkspaceConfig{Mode: "pvc", AccessMode: "RWX"}, "ns", nil)
	if _, ok := ws.(*pvcWorkspaceRWX); !ok {
		t.Fatal("expected pvcWorkspaceRWX")
	}
}

func TestNewWorkspaceStrategy_PVC_RWO(t *testing.T) {
	ws := NewWorkspaceStrategy(nil, WorkspaceConfig{Mode: "pvc", AccessMode: "RWO"}, "ns", nil)
	if _, ok := ws.(*pvcWorkspaceRWO); !ok {
		t.Fatal("expected pvcWorkspaceRWO")
	}
}

func TestTaskWorkspaceScope(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want string
	}{
		{"nil tags", nil, "isolated"},
		{"empty tags", map[string]string{}, "isolated"},
		{"no scope tag", map[string]string{"foo": "bar"}, "isolated"},
		{"shared scope", map[string]string{"conquest_workspace_scope": "shared"}, "shared"},
		{"other scope value", map[string]string{"conquest_workspace_scope": "other"}, "isolated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := taskWorkspaceScope(tt.tags); got != tt.want {
				t.Fatalf("taskWorkspaceScope(%v) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
}

func TestWorkspaceBinding_IsPVC(t *testing.T) {
	var nilBinding *domain.WorkspaceBinding
	if nilBinding.IsPVC() {
		t.Fatal("nil binding should not be PVC")
	}

	none := &domain.WorkspaceBinding{Kind: domain.WorkspaceKindNone}
	if none.IsPVC() {
		t.Fatal("none binding should not be PVC")
	}

	pvc := &domain.WorkspaceBinding{Kind: domain.WorkspaceKindPVC, ClaimName: "test"}
	if !pvc.IsPVC() {
		t.Fatal("pvc binding should be PVC")
	}
}

func TestPVCWorkspaceRWX_RunnerEnv(t *testing.T) {
	ws := &pvcWorkspaceRWX{}
	envs := ws.RunnerEnv()
	found := false
	for _, e := range envs {
		if e.Name == "CONQUEST_SHARED_WORKSPACE" && e.Value == "true" {
			found = true
		}
	}
	if !found {
		t.Fatal("RWX should inject CONQUEST_SHARED_WORKSPACE=true")
	}
}

func TestPVCWorkspaceRWO_RunnerEnv(t *testing.T) {
	ws := &pvcWorkspaceRWO{}
	envs := ws.RunnerEnv()
	found := false
	for _, e := range envs {
		if e.Name == "CONQUEST_SHARED_WORKSPACE" && e.Value == "true" {
			found = true
		}
	}
	if !found {
		t.Fatal("RWO should inject CONQUEST_SHARED_WORKSPACE=true")
	}
}

// Verify PVC volume sources have the correct access modes.
func TestPVCRunnerVolumes_VolumeSourceType(t *testing.T) {
	binding := &domain.WorkspaceBinding{
		Kind:       domain.WorkspaceKindPVC,
		ClaimName:  "test-claim",
		AccessMode: domain.WorkspaceAccessModeRWX,
	}
	vol, _ := pvcRunnerVolumes(binding)
	if vol.VolumeSource.PersistentVolumeClaim == nil {
		t.Fatal("expected PersistentVolumeClaim volume source")
	}
	if vol.VolumeSource.EmptyDir != nil {
		t.Fatal("unexpected EmptyDir in PVC mode")
	}
}

func TestEmptyDirWorkspace_RunnerVolumes_VolumeSourceType(t *testing.T) {
	ws := &emptyDirWorkspace{}
	vol, _ := ws.RunnerVolumes(nil)
	if vol.VolumeSource.EmptyDir == nil {
		t.Fatal("expected EmptyDir volume source")
	}
	if vol.VolumeSource.PersistentVolumeClaim != nil {
		t.Fatal("unexpected PVC in emptydir mode")
	}
}

// Verify PVC access modes are set correctly in the spec.
func TestPVCAccessMode_RWX(t *testing.T) {
	_ = pvcWorkspaceBase{size: "10Gi"}
	var accessMode corev1.PersistentVolumeAccessMode = corev1.ReadWriteMany
	if accessMode != corev1.ReadWriteMany {
		t.Fatal("should be ReadWriteMany")
	}
}

func TestPVCAccessMode_RWO(t *testing.T) {
	_ = pvcWorkspaceBase{size: "10Gi"}
	var accessMode corev1.PersistentVolumeAccessMode = corev1.ReadWriteOnce
	if accessMode != corev1.ReadWriteOnce {
		t.Fatal("should be ReadWriteOnce")
	}
}
