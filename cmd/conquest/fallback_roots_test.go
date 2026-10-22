package main

import (
	"crypto/x509"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// TestFallbackRootsWithoutSystemBundle runs a child with SSL_CERT_FILE and
// SSL_CERT_DIR pointed at nothing, which is what an executor image without
// a CA bundle looks like. The binary must still have roots to verify S3 TLS.
func TestFallbackRootsWithoutSystemBundle(t *testing.T) {
	if os.Getenv("CONQUEST_FALLBACK_ROOTS_CHILD") == "1" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			t.Fatalf("system cert pool: %v", err)
		}
		// Subjects is deprecated for system pools, but the fallback is a plain pool.
		if n := len(pool.Subjects()); n == 0 {
			t.Fatal("no trusted roots without a system CA bundle")
		}
		return
	}
	if runtime.GOOS != "linux" {
		t.Skip("SSL_CERT_FILE and SSL_CERT_DIR only control the root pool on Linux")
	}

	empty := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFallbackRootsWithoutSystemBundle$")
	cmd.Env = append(os.Environ(),
		"CONQUEST_FALLBACK_ROOTS_CHILD=1",
		"SSL_CERT_FILE="+empty+"/none.pem",
		"SSL_CERT_DIR="+empty,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
}
