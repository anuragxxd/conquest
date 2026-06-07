package source

import (
	"testing"
)

func TestValidateGitHubURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "basic repo",
			url:  "https://github.com/anuragxxd/conquest-acceptance",
			want: "https://github.com/anuragxxd/conquest-acceptance",
		},
		{
			name: "with .git suffix",
			url:  "https://github.com/anuragxxd/conquest-acceptance.git",
			want: "https://github.com/anuragxxd/conquest-acceptance",
		},
		{
			name: "trailing slash",
			url:  "https://github.com/anuragxxd/conquest-acceptance/",
			want: "https://github.com/anuragxxd/conquest-acceptance",
		},
		{
			name: "trailing slash with .git",
			url:  "https://github.com/nextflow-io/hello.git",
			want: "https://github.com/nextflow-io/hello",
		},
		{
			name:    "empty",
			url:     "",
			wantErr: true,
		},
		{
			name:    "ssh git url",
			url:     "git@github.com:anuragxxd/infra.git",
			wantErr: true,
		},
		{
			name:    "s3 url",
			url:     "s3://conquest/workflows/main.nf",
			wantErr: true,
		},
		{
			name:    "trs url",
			url:     "trs://example.com/tool/1/version/1",
			wantErr: true,
		},
		{
			name:    "http scheme",
			url:     "http://github.com/org/repo",
			wantErr: true,
		},
		{
			name:    "non-github host",
			url:     "https://gitlab.com/org/repo",
			wantErr: true,
		},
		{
			name:    "blob sub-path",
			url:     "https://github.com/org/repo/blob/main/main.nf",
			wantErr: true,
		},
		{
			name:    "tree sub-path",
			url:     "https://github.com/org/repo/tree/main",
			wantErr: true,
		},
		{
			name:    "only org, no repo",
			url:     "https://github.com/anuragxxd",
			wantErr: true,
		},
		{
			name:    "three path segments",
			url:     "https://github.com/org/repo/extra",
			wantErr: true,
		},
		{
			name:    "query string",
			url:     "https://github.com/org/repo?tab=readme",
			wantErr: true,
		},
		{
			name:    "fragment",
			url:     "https://github.com/org/repo#readme",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateGitHubURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateGitHubURL(%q) expected error, got %q", tt.url, got)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateGitHubURL(%q) unexpected error: %v", tt.url, err)
				return
			}
			if got != tt.want {
				t.Errorf("ValidateGitHubURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestValidateRevision(t *testing.T) {
	tests := []struct {
		name    string
		rev     string
		wantErr bool
	}{
		{
			name: "valid sha",
			rev:  "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2",
		},
		{
			name:    "empty",
			rev:     "",
			wantErr: true,
		},
		{
			name:    "short sha",
			rev:     "a3f5c8e",
			wantErr: true,
		},
		{
			name:    "tag",
			rev:     "v1.0.0",
			wantErr: true,
		},
		{
			name:    "branch",
			rev:     "main",
			wantErr: true,
		},
		{
			name:    "uppercase hex",
			rev:     "A3F5C8E1B2D4F6A8C0E2D4F6A8B0C2D4E6F8A0B2",
			wantErr: true,
		},
		{
			name:    "41 chars",
			rev:     "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2a",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRevision(tt.rev)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ValidateRevision(%q) expected error", tt.rev)
				}
				return
			}
			if err != nil {
				t.Errorf("ValidateRevision(%q) unexpected error: %v", tt.rev, err)
			}
		})
	}
}

func TestNormalizeProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profiles []string
		want     []string
		wantErr  bool
	}{
		{
			name:     "nil profiles",
			profiles: nil,
			want:     nil,
		},
		{
			name:     "trim whitespace",
			profiles: []string{" docker ", "test"},
			want:     []string{"docker", "test"},
		},
		{
			name:     "empty profile",
			profiles: []string{"docker", "  "},
			wantErr:  true,
		},
		{
			name:     "profile contains comma",
			profiles: []string{"docker,test"},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeProfiles(tt.profiles)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("len(got) = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestResolve(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		params := &NextflowEngineParams{
			Revision: "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2",
			Entry:    "myWorkflow",
			Profiles: []string{"docker", "test"},
		}
		src, err := Resolve("https://github.com/anuragxxd/conquest-acceptance.git", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if src.Type != TypeGitHubHTTPS {
			t.Errorf("Type = %q, want %q", src.Type, TypeGitHubHTTPS)
		}
		if src.RepoURL != "https://github.com/anuragxxd/conquest-acceptance" {
			t.Errorf("RepoURL = %q", src.RepoURL)
		}
		if src.Revision != params.Revision {
			t.Errorf("Revision = %q", src.Revision)
		}
		wantRef := "https://github.com/anuragxxd/conquest-acceptance@a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2"
		if src.Ref != wantRef {
			t.Errorf("Ref = %q, want %q", src.Ref, wantRef)
		}
	})

	t.Run("nil params", func(t *testing.T) {
		_, err := Resolve("https://github.com/org/repo", nil)
		if err == nil {
			t.Error("expected error for nil params")
		}
	})

	t.Run("bad url", func(t *testing.T) {
		params := &NextflowEngineParams{Revision: "a3f5c8e1b2d4f6a8c0e2d4f6a8b0c2d4e6f8a0b2"}
		_, err := Resolve("git@github.com:org/repo.git", params)
		if err == nil {
			t.Error("expected error for SSH url")
		}
	})

	t.Run("bad revision", func(t *testing.T) {
		params := &NextflowEngineParams{Revision: "main"}
		_, err := Resolve("https://github.com/org/repo", params)
		if err == nil {
			t.Error("expected error for branch revision")
		}
	})
}
