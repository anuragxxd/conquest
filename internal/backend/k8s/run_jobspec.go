package k8s

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
)

const LabelJobType = "conquest.io/job-type"

func buildRunnerJobSpec(
	run *domain.Run,
	cfg RunConfig,
	spec *backend.RunnerSpec,
	ws WorkspaceStrategy,
	binding *domain.WorkspaceBinding,
) (*batchv1.Job, error) {
	if spec == nil {
		return nil, fmt.Errorf("runner spec is required")
	}
	if spec.Image == "" {
		return nil, fmt.Errorf("runner image is required")
	}
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("runner command is required")
	}

	jobName := sanitizeK8sName("conquest-runner-" + run.RunID)

	var backoffLimit int32 = 0
	var completions int32 = 1
	var ttlSeconds int32 = 3600

	labels := map[string]string{
		LabelRunID:   run.RunID,
		LabelJobType: "runner",
		LabelBackend: "k8s",
		LabelManaged: "conquest",
	}

	// Build env vars: spec env + storage secrets + workspace env.
	envVars := make([]corev1.EnvVar, 0, len(spec.Env)+4)
	for _, env := range spec.Env {
		envVars = append(envVars, corev1.EnvVar{Name: env.Name, Value: env.Value})
	}
	envVars = append(envVars, awsSecretEnvVars(cfg.StorageSecretName)...)

	// Inject workspace capability flag for PVC modes (Phase 1b).
	if wsEnv := ws.RunnerEnv(); len(wsEnv) > 0 {
		envVars = append(envVars, wsEnv...)
	}

	// Build volumes and mounts. /workspace comes from the workspace strategy;
	// /tmp and /.nextflow (Nextflow only) are always emptyDir.
	workspaceVol, workspaceMount := ws.RunnerVolumes(binding)
	volumes := []corev1.Volume{
		workspaceVol,
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	volumeMounts := []corev1.VolumeMount{workspaceMount, {Name: "tmp", MountPath: "/tmp"}}

	if run.WorkflowEngine == "nextflow" {
		volumes = append(volumes, corev1.Volume{Name: "engine-home", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "engine-home", MountPath: "/.nextflow"})
	}

	ephemeralRequest := cfg.EphemeralStorageRequest
	if ephemeralRequest == "" {
		ephemeralRequest = "20Gi"
	}
	ephemeralLimit := cfg.EphemeralStorageLimit
	if ephemeralLimit == "" {
		ephemeralLimit = "40Gi"
	}

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
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: cfg.ServiceAccount,
					SecurityContext:    podSecurityContextForWorkspace(binding),
					ImagePullSecrets:   imagePullSecretRefs(cfg.ImagePullSecrets),
					Volumes:            volumes,
					Containers: []corev1.Container{
						{
							Name:         "runner",
							Image:        spec.Image,
							Command:      append([]string(nil), spec.Command...),
							Args:         append([]string(nil), spec.Args...),
							Env:          envVars,
							VolumeMounts: volumeMounts,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("500m"),
									corev1.ResourceMemory:           resource.MustParse("1Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse(ephemeralRequest),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:              resource.MustParse("2"),
									corev1.ResourceMemory:           resource.MustParse("4Gi"),
									corev1.ResourceEphemeralStorage: resource.MustParse(ephemeralLimit),
								},
							},
						},
					},
				},
			},
		},
	}

	return job, nil
}
