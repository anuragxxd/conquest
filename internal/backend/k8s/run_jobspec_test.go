package k8s

import (
	"testing"

	"github.com/anuragxxd/conquest/internal/backend"
	"github.com/anuragxxd/conquest/internal/domain"
)

func TestBuildRunnerJobSpec_UsesProvidedRunnerSpec(t *testing.T) {
	run := domain.NewRun("NFL", "DSL2", "nextflow", "24.10.4", nil, "", "v1", "")

	job, err := buildRunnerJobSpec(run, RunConfig{
		Namespace:               "services",
		ServiceAccount:          "conquest",
		StorageSecretName:       "custom-storage",
		ImagePullSecrets:        []string{"registry-a", "registry-b"},
		EphemeralStorageRequest: "12Gi",
		EphemeralStorageLimit:   "24Gi",
	}, &backend.RunnerSpec{
		Image:   "example.com/conquest-runner:nf-25.04.3",
		Command: []string{"/usr/local/bin/conquest"},
		Args:    []string{"runner", "nextflow", "--run-id", run.RunID},
		Env: []backend.EnvVar{
			{Name: "CONQUEST_WORKFLOW_PARAMS", Value: `{"hello":"world"}`},
			{Name: "NXF_PLUGINS_ALLOWED", Value: "nf-ga4gh,nf-amazon"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	container := job.Spec.Template.Spec.Containers[0]
	if got := container.Image; got != "example.com/conquest-runner:nf-25.04.3" {
		t.Fatalf("runner image = %q", got)
	}
	if got := container.Command[0]; got != "/usr/local/bin/conquest" {
		t.Fatalf("runner command = %q", got)
	}
	if got := container.Args[0]; got != "runner" {
		t.Fatalf("runner arg[0] = %q", got)
	}

	if len(job.Spec.Template.Spec.ImagePullSecrets) != 2 {
		t.Fatalf("imagePullSecrets = %d, want 2", len(job.Spec.Template.Spec.ImagePullSecrets))
	}

	foundParams := false
	foundAllowedPlugins := false
	foundAccessKey := false
	for _, env := range container.Env {
		switch env.Name {
		case "CONQUEST_WORKFLOW_PARAMS":
			foundParams = env.Value == `{"hello":"world"}`
		case "NXF_PLUGINS_ALLOWED":
			foundAllowedPlugins = env.Value == "nf-ga4gh,nf-amazon"
		case "AWS_ACCESS_KEY_ID":
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				foundAccessKey = env.ValueFrom.SecretKeyRef.Name == "custom-storage"
			}
		}
	}

	if !foundParams {
		t.Fatal("runner env missing CONQUEST_WORKFLOW_PARAMS")
	}
	if !foundAllowedPlugins {
		t.Fatal("runner env missing NXF_PLUGINS_ALLOWED")
	}
	if !foundAccessKey {
		t.Fatal("runner env missing AWS_ACCESS_KEY_ID from configured secret")
	}
	if got := container.Resources.Requests.StorageEphemeral().String(); got != "12Gi" {
		t.Fatalf("ephemeral request = %q", got)
	}
	if got := container.Resources.Limits.StorageEphemeral().String(); got != "24Gi" {
		t.Fatalf("ephemeral limit = %q", got)
	}
}

func TestBuildRunnerJobSpec_RequiresImage(t *testing.T) {
	run := domain.NewRun("NFL", "DSL2", "nextflow", "24.10.4", nil, "", "v1", "")

	_, err := buildRunnerJobSpec(run, RunConfig{}, &backend.RunnerSpec{Command: []string{"/usr/local/bin/conquest"}})
	if err == nil {
		t.Fatal("expected error for missing runner image")
	}
}

func TestBuildRunnerJobSpec_OmitsNextflowHomeForSnakemake(t *testing.T) {
	run := domain.NewRun("SMK", "9.17.3", "snakemake", "9.17.3", nil, "", "v1", "")

	job, err := buildRunnerJobSpec(run, RunConfig{}, &backend.RunnerSpec{
		Image:   "example.com/conquest-snakemake-runner:latest",
		Command: []string{"/usr/local/bin/conquest"},
		Args:    []string{"runner", "snakemake"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, mount := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.MountPath == "/.nextflow" {
			t.Fatal("snakemake runner should not mount /.nextflow")
		}
	}
}
