package k8s

import (
	"encoding/json"
	"testing"

	"github.com/anuragxxd/conquest/internal/domain"
	corev1 "k8s.io/api/core/v1"
)

func TestBuildJobSpec_MountsSharedBucketPathAndUsesToolsBinary(t *testing.T) {
	tesPayload := json.RawMessage(`{
		"name": "test-task",
		"executors": [
			{
				"image": "ubuntu:24.04",
				"command": ["bash", ".command.run"],
				"workdir": "/work"
			}
		],
		"inputs": [
			{
				"url": "s3://conquest/v1/workflow-runs/run/work/nextflow/tmp/x/bin/bactopia-prokka",
				"path": "/conquest/v1/workflow-runs/run/work/nextflow/tmp/x/bin/bactopia-prokka"
			}
		]
	}`)

	task := domain.NewTask("run-123")
	task.TESTaskID = "tes-123"
	task.TaskRequestPayload = tesPayload

	job, err := buildJobSpec(task, Config{
		Namespace:     "services",
		ConquestImage: "example.com/conquest:latest",
		S3Bucket:      "conquest",
		S3Endpoint:    "http://minio:9000",
		S3Region:      "us-east-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(job.Spec.Template.Spec.InitContainers) < 2 {
		t.Fatalf("expected stage-in init container")
	}
	copyTools := job.Spec.Template.Spec.InitContainers[0]
	if got := copyTools.Command[1]; got != "/usr/local/bin/conquest" {
		t.Fatalf("copy-tools source = %q, want /usr/local/bin/conquest", got)
	}
	stageIn := job.Spec.Template.Spec.InitContainers[1]
	if got := stageIn.Command[0]; got != "/tools/conquest" {
		t.Fatalf("stage-in command = %q, want /tools/conquest", got)
	}

	hasBucketMount := func(mounts []struct{ name, path string }) bool {
		for _, mount := range mounts {
			if mount.name == "workdir" && mount.path == "/conquest" {
				return true
			}
		}
		return false
	}

	stageInMounts := make([]struct{ name, path string }, 0, len(stageIn.VolumeMounts))
	for _, mount := range stageIn.VolumeMounts {
		stageInMounts = append(stageInMounts, struct{ name, path string }{name: mount.Name, path: mount.MountPath})
	}
	if !hasBucketMount(stageInMounts) {
		t.Fatalf("stage-in mounts missing shared bucket path: %+v", stageIn.VolumeMounts)
	}

	executor := job.Spec.Template.Spec.Containers[0]
	executorMounts := make([]struct{ name, path string }, 0, len(executor.VolumeMounts))
	for _, mount := range executor.VolumeMounts {
		executorMounts = append(executorMounts, struct{ name, path string }{name: mount.Name, path: mount.MountPath})
	}
	if !hasBucketMount(executorMounts) {
		t.Fatalf("executor mounts missing shared bucket path: %+v", executor.VolumeMounts)
	}
}

func TestBuildJobSpec_MountsCommonWorkspaceForNestedWorkdir(t *testing.T) {
	tesPayload := json.RawMessage(`{
		"name": "test-task",
		"executors": [
			{
				"image": "ubuntu:24.04",
				"command": ["bash", "-lc", "echo hi"],
				"workdir": "/work/workflow/.test"
			}
		],
		"inputs": [
			{
				"url": "s3://conquest/v1/workflow-runs/run/workflow/bundle/",
				"path": "/work/workflow",
				"type": "DIRECTORY"
			}
		],
		"outputs": [
			{
				"url": "s3://conquest/v1/workflow-runs/run/outputs/.test/results/out.txt",
				"path": "/work/workflow/.test/results/out.txt",
				"type": "FILE"
			}
		]
	}`)

	task := domain.NewTask("run-123")
	task.TESTaskID = "tes-123"
	task.TaskRequestPayload = tesPayload

	job, err := buildJobSpec(task, Config{
		Namespace:     "services",
		ConquestImage: "example.com/conquest:latest",
		S3Bucket:      "conquest",
		S3Endpoint:    "http://minio:9000",
		S3Region:      "us-east-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stageIn := job.Spec.Template.Spec.InitContainers[1]
	executor := job.Spec.Template.Spec.Containers[0]

	hasWorkspaceMount := func(mounts []corev1.VolumeMount) bool {
		for _, mount := range mounts {
			if mount.Name == "workdir" && mount.MountPath == "/work/workflow" {
				return true
			}
		}
		return false
	}

	if !hasWorkspaceMount(stageIn.VolumeMounts) {
		t.Fatalf("stage-in mounts missing /work/workflow: %+v", stageIn.VolumeMounts)
	}
	if !hasWorkspaceMount(executor.VolumeMounts) {
		t.Fatalf("executor mounts missing /work/workflow: %+v", executor.VolumeMounts)
	}
}

func TestBuildJobSpec_SetsEphemeralStorageFromTESDisk(t *testing.T) {
	tesPayload := json.RawMessage(`{
		"name": "test-task",
		"executors": [{"image": "ubuntu:24.04", "command": ["bash", "-c", "echo hi"], "workdir": "/work"}],
		"resources": {"disk_gb": 8}
	}`)

	task := domain.NewTask("run-123")
	task.TESTaskID = "tes-123"
	task.TaskRequestPayload = tesPayload

	job, err := buildJobSpec(task, Config{
		Namespace:     "services",
		ConquestImage: "example.com/conquest:latest",
		S3Bucket:      "conquest",
		S3Endpoint:    "http://minio:9000",
		S3Region:      "us-east-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	executor := job.Spec.Template.Spec.Containers[0]
	if got := executor.Resources.Requests.StorageEphemeral().String(); got != "8Gi" {
		t.Fatalf("ephemeral request = %q", got)
	}
	if got := executor.Resources.Limits.StorageEphemeral().String(); got != "8Gi" {
		t.Fatalf("ephemeral limit = %q", got)
	}
}

func TestBuildJobSpec_NormalizesExecutorImageRef(t *testing.T) {
	tesPayload := json.RawMessage(`{
		"name": "test-task",
		"executors": [{"image": "docker://ubuntu:24.04", "command": ["bash", "-c", "echo hi"], "workdir": "/work"}]
	}`)

	task := domain.NewTask("run-123")
	task.TESTaskID = "tes-123"
	task.TaskRequestPayload = tesPayload

	job, err := buildJobSpec(task, Config{
		Namespace:     "services",
		ConquestImage: "example.com/conquest:latest",
		S3Bucket:      "conquest",
		S3Endpoint:    "http://minio:9000",
		S3Region:      "us-east-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := job.Spec.Template.Spec.Containers[0].Image; got != "ubuntu:24.04" {
		t.Fatalf("executor image = %q, want ubuntu:24.04", got)
	}
}

func TestBuildJobSpec_RejectsUnsupportedExecutorImageScheme(t *testing.T) {
	tesPayload := json.RawMessage(`{
		"name": "test-task",
		"executors": [{"image": "shub://library/default/image:latest", "command": ["bash", "-c", "echo hi"], "workdir": "/work"}]
	}`)

	task := domain.NewTask("run-123")
	task.TESTaskID = "tes-123"
	task.TaskRequestPayload = tesPayload

	_, err := buildJobSpec(task, Config{
		Namespace:     "services",
		ConquestImage: "example.com/conquest:latest",
		S3Bucket:      "conquest",
		S3Endpoint:    "http://minio:9000",
		S3Region:      "us-east-1",
	})
	if err == nil {
		t.Fatal("expected unsupported image scheme to be rejected")
	}
}
