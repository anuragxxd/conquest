// Package k8s implements the TaskBackend interface using Kubernetes Jobs.
//
// Each TES task becomes one K8s Job with:
//   - An init container (conquest image) that downloads TES inputs from S3
//   - A main container (user executor image) that runs the TES command
//   - A workspace volume (emptyDir or PVC subpath) at the executor workdir
//   - A shared tools volume for the conquest bootstrap binary
//
// After the main container exits, the reconciler picks up the terminal
// state and triggers output upload from the control plane.
package k8s

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/anuragxxd/conquest/internal/api/tes"
	"github.com/anuragxxd/conquest/internal/domain"
	"github.com/anuragxxd/conquest/internal/taskpolicy"
)

// Labels applied to all conquest-managed K8s objects.
const (
	LabelTaskID  = "conquest.io/task-id"
	LabelRunID   = "conquest.io/run-id"
	LabelAttempt = "conquest.io/attempt"
	LabelBackend = "conquest.io/backend"
	LabelManaged = "app.kubernetes.io/managed-by"
)

// Config holds K8s backend configuration.
type Config struct {
	Namespace         string // K8s namespace for task Jobs
	ConquestImage     string // Image with the conquest binary for init/filer
	S3Endpoint        string // MinIO/S3 endpoint URL
	S3Bucket          string // Default bucket
	S3Region          string // Region
	ServiceAccount    string // SA for task pods (empty = use namespace default)
	StorageSecretName string
	ImagePullSecrets  []string
	Workspace         WorkspaceConfig
}

// buildJobSpec creates a K8s Job spec from a TES task.
func buildJobSpec(task *domain.Task, cfg Config, ws WorkspaceStrategy) (*batchv1.Job, error) {
	// Parse the TES request from the stored payload.
	var tesTask tes.Task
	if err := json.Unmarshal(task.TaskRequestPayload, &tesTask); err != nil {
		return nil, fmt.Errorf("unmarshal task request: %w", err)
	}

	if len(tesTask.Executors) == 0 {
		return nil, fmt.Errorf("task has no executors")
	}

	// v1 scope: single executor only.
	exec := tesTask.Executors[0]
	normalizedImage, err := taskpolicy.NormalizeImageRef(exec.Image)
	if err != nil {
		return nil, fmt.Errorf("normalize executor image: %w", err)
	}
	if normalizedImage == "" {
		return nil, fmt.Errorf("executor image is required")
	}
	exec.Image = normalizedImage
	workdir := exec.Workdir
	if workdir == "" {
		workdir = "/work"
	}

	jobName := fmt.Sprintf("conquest-task-%s", task.TESTaskID)
	// K8s names must be DNS-safe.
	jobName = sanitizeK8sName(jobName)

	var backoffLimit int32 = 0
	var completions int32 = 1
	var ttlSeconds int32 = 600 // clean up finished Jobs after 10 min

	// Build the input/output manifest as JSON for the init container.
	manifest := StageManifest{
		Inputs:  tesTask.Inputs,
		Outputs: tesTask.Outputs,
		Workdir: workdir,
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal stage manifest: %w", err)
	}

	labels := map[string]string{
		LabelTaskID:  task.TESTaskID,
		LabelRunID:   task.RunID,
		LabelAttempt: fmt.Sprintf("%d", task.Attempt),
		LabelBackend: "k8s",
		LabelManaged: "conquest",
	}

	// Build resource requirements for the executor container.
	execResources := buildResourceRequirements(tesTask.Resources)

	// Build env vars for the executor container.
	var envVars []corev1.EnvVar
	for k, v := range exec.Env {
		envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
	}

	// Determine workspace scope from TES tags.
	scope := taskWorkspaceScope(tesTask.Tags)

	// Compute mount path for workspace volume.
	workspaceMount := taskWorkspaceMountPath(workdir, tesTask.Inputs, tesTask.Outputs)

	// Build workspace volume + mount using strategy.
	workdirVol, workdirMount := ws.TaskVolumes(task.Workspace, task.TESTaskID, workspaceMount, scope)

	// Build the extra bucket mount if needed (existing behavior: mount at
	// /<bucket> if inputs reference paths under /<bucket>).
	sharedBucketMount := "/" + strings.Trim(cfg.S3Bucket, "/")
	workdirMounts := []corev1.VolumeMount{workdirMount}
	if scope == "shared" && workspaceMount != workdirMount.MountPath {
		workdirMounts = append(workdirMounts, corev1.VolumeMount{
			Name:      workdirMount.Name,
			MountPath: workspaceMount,
			SubPath:   workdirMount.SubPath,
		})
	}
	if sharedBucketMount != "/" && sharedBucketMount != workspaceMount {
		workdirMounts = append(workdirMounts, corev1.VolumeMount{Name: "workdir", MountPath: sharedBucketMount})
	}

	// Build volumes list: workspace + stage manifest + tools.
	volumes := []corev1.Volume{
		workdirVol,
		{
			Name: "stage-manifest",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "tools",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Build pod affinity for RWO co-location (nil for other modes).
	affinity := ws.TaskAffinity(task.Workspace, task.RunID)

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: cfg.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			Completions:             &completions,
			TTLSecondsAfterFinished: &ttlSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: cfg.ServiceAccount,
					SecurityContext:    podSecurityContextForWorkspace(task.Workspace),
					ImagePullSecrets:   imagePullSecretRefs(cfg.ImagePullSecrets),
					Affinity:           affinity,

					Volumes: volumes,

					// Init containers:
					// 1. Copy conquest binary to shared tools volume.
					// 2. Download TES inputs from S3 into workdir.
					InitContainers: []corev1.Container{
						{
							Name:    "copy-tools",
							Image:   cfg.ConquestImage,
							Command: []string{"cp", "/usr/local/bin/conquest", "/tools/conquest"},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "tools", MountPath: "/tools"},
							},
						},
						{
							Name:  "stage-in",
							Image: cfg.ConquestImage,
							Command: []string{
								"/tools/conquest", "filer", "stage-in",
								"--manifest", "/stage/manifest.json",
							},
							Env: append([]corev1.EnvVar{
								{Name: "CONQUEST_S3_ENDPOINT", Value: cfg.S3Endpoint},
								{Name: "CONQUEST_S3_BUCKET", Value: cfg.S3Bucket},
								{Name: "CONQUEST_S3_REGION", Value: cfg.S3Region},
								{Name: "STAGE_MANIFEST", Value: string(manifestJSON)},
							}, awsSecretEnvVars(cfg.StorageSecretName)...),
							VolumeMounts: append(append([]corev1.VolumeMount{}, workdirMounts...),
								corev1.VolumeMount{Name: "stage-manifest", MountPath: "/stage"},
								corev1.VolumeMount{Name: "tools", MountPath: "/tools"},
							),
						},
					},

					// Main container: runs through the conquest exec wrapper
					// which stages out outputs after the command finishes.
					Containers: []corev1.Container{
						{
							Name:  "executor",
							Image: exec.Image,
							// Wrap the user command through the conquest exec wrapper
							// so outputs are uploaded before the container exits.
							Command:    append([]string{"/tools/conquest", "filer", "exec", "--"}, exec.Command...),
							WorkingDir: workdir,
							Env: append(
								append(
									append([]corev1.EnvVar{}, envVars...),
									corev1.EnvVar{Name: "STAGE_MANIFEST", Value: string(manifestJSON)},
									corev1.EnvVar{Name: "CONQUEST_S3_ENDPOINT", Value: cfg.S3Endpoint},
									corev1.EnvVar{Name: "CONQUEST_S3_BUCKET", Value: cfg.S3Bucket},
									corev1.EnvVar{Name: "CONQUEST_S3_REGION", Value: cfg.S3Region},
								),
								awsSecretEnvVars(cfg.StorageSecretName)...,
							),
							Resources: execResources,
							VolumeMounts: append(append([]corev1.VolumeMount{}, workdirMounts...),
								corev1.VolumeMount{Name: "tools", MountPath: "/tools"},
								corev1.VolumeMount{Name: "stage-manifest", MountPath: "/stage"},
							),
						},
					},
				},
			},
		},
	}

	return job, nil
}

// taskWorkspaceScope reads the workspace scope from TES task tags.
// Returns "shared" if the tag conquest_workspace_scope=shared is set,
// "isolated" otherwise.
func taskWorkspaceScope(tags map[string]string) string {
	if tags != nil && tags["conquest_workspace_scope"] == "shared" {
		return "shared"
	}
	return "isolated"
}

// buildResourceRequirements converts TES resources to K8s resource requirements.
func buildResourceRequirements(res *tes.Resources) corev1.ResourceRequirements {
	reqs := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}
	if res == nil {
		return reqs
	}

	if res.CPUCores != nil && *res.CPUCores > 0 {
		cpu := resource.NewMilliQuantity(int64(*res.CPUCores)*1000, resource.DecimalSI)
		reqs.Requests[corev1.ResourceCPU] = *cpu
		reqs.Limits[corev1.ResourceCPU] = *cpu
	}

	if res.RAMGb != nil && *res.RAMGb > 0 {
		mem := resource.NewQuantity(int64(*res.RAMGb*1024*1024*1024), resource.BinarySI)
		reqs.Requests[corev1.ResourceMemory] = *mem
		reqs.Limits[corev1.ResourceMemory] = *mem
	}

	if res.DiskGb != nil && *res.DiskGb > 0 {
		disk := resource.NewQuantity(int64(*res.DiskGb*1024*1024*1024), resource.BinarySI)
		reqs.Requests[corev1.ResourceEphemeralStorage] = *disk
		reqs.Limits[corev1.ResourceEphemeralStorage] = *disk
	}

	return reqs
}

// sanitizeK8sName makes a string safe for K8s resource names.
func sanitizeK8sName(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	result := b.String()
	// K8s names must start/end with alphanumeric.
	result = strings.Trim(result, "-")
	if len(result) > 63 {
		result = result[:63]
	}
	return result
}

// StageManifest describes the inputs and outputs for the stage-in init container.
type StageManifest struct {
	Inputs  []tes.Input  `json:"inputs,omitempty"`
	Outputs []tes.Output `json:"outputs,omitempty"`
	Workdir string       `json:"workdir"`
}

func taskWorkspaceMountPath(workdir string, inputs []tes.Input, outputs []tes.Output) string {
	paths := []string{workdir}
	for _, input := range inputs {
		if input.Path != "" {
			paths = append(paths, input.Path)
		}
	}
	for _, output := range outputs {
		if output.Path != "" {
			paths = append(paths, output.Path)
		}
	}
	common := commonPathPrefix(paths)
	if common == "" || common == "/" {
		common = path.Clean(workdir)
	}
	if common == "." || common == "" {
		return "/work"
	}
	return common
}

func commonPathPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	segments := splitPath(paths[0])
	for _, raw := range paths[1:] {
		current := splitPath(raw)
		i := 0
		for i < len(segments) && i < len(current) && segments[i] == current[i] {
			i++
		}
		segments = segments[:i]
		if len(segments) == 0 {
			return "/"
		}
	}
	return "/" + path.Join(segments...)
}

func splitPath(raw string) []string {
	cleaned := path.Clean(raw)
	if cleaned == "/" || cleaned == "." || cleaned == "" {
		return nil
	}
	return strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
}
