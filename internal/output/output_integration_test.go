//go:build integration

package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationNoOverwriteCollisionRefusal(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "source.mp4")
	dst := filepath.Join(root, "target.mp4")
	if err := os.WriteFile(src, []byte("src-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := Destination{Path: dst, Dir: root, Container: "mp4"}

	part, err := Temp(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(part)
	if err := os.WriteFile(part, []byte("new-encode"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Destination appears concurrently before publish.
	if err := os.WriteFile(dst, []byte("concurrent-winner"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = Publish(part, dest, src, false)
	if err == nil || !strings.Contains(err.Error(), "appeared") {
		t.Fatalf("expected no-overwrite collision refusal, got %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "concurrent-winner" {
		t.Fatalf("existing destination was overwritten: got %q", got)
	}
}
