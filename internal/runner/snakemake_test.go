package runner

import "testing"

func TestBuildSnakemakeArgs_InsertsDoubleDashBeforeTargets(t *testing.T) {
	args := buildSnakemakeArgs(SnakemakeConfig{
		Jobs:          2,
		Snakefile:     "workflow/Snakefile",
		Directory:     ".test",
		Targets:       []string{"results/report.html", "results/extra.txt"},
		DefaultDiskMB: 8192,
	}, "workflow/config.json")

	var foundDoubleDash bool
	for i := 0; i < len(args); i++ {
		if args[i] != "--" {
			continue
		}
		foundDoubleDash = true
		if i+2 >= len(args) {
			t.Fatalf("targets missing after --: %v", args)
		}
		if args[i+1] != "results/report.html" || args[i+2] != "results/extra.txt" {
			t.Fatalf("unexpected targets after --: %v", args[i+1:])
		}
		break
	}

	if !foundDoubleDash {
		t.Fatalf("args missing -- before targets: %v", args)
	}
}

func TestBuildSnakemakeArgs_OmitsDoubleDashWithoutTargets(t *testing.T) {
	args := buildSnakemakeArgs(SnakemakeConfig{Jobs: 1, DefaultDiskMB: 8192}, "")
	for _, arg := range args {
		if arg == "--" {
			t.Fatalf("unexpected -- without targets: %v", args)
		}
	}
}
