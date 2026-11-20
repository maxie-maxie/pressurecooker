package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maxie-maxie/pressurecooker/internal/config"
)

func TestContainerConflictWithExtension(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "clip.mp4")
	s := config.Factory()
	s.Container = "mp4"

	_, err := Resolve(src, filepath.Join(root, "out.mkv"), s, "default", int64(20000000), false)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected container conflict error, got %v", err)
	}
}

func TestContainerMatchingExtensionAccepted(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "clip.mp4")
	s := config.Factory()
	s.Container = "mkv"

	dest, err := Resolve(src, filepath.Join(root, "out.mkv"), s, "default", int64(20000000), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest.Container != "mkv" || filepath.Base(dest.Path) != "out.mkv" {
		t.Fatalf("unexpected destination: %+v", dest)
	}
}

func TestUnrecognizedExtensionTreatedAsDirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "clip.mp4")
	s := config.Factory()
	targetBytes := int64(20000000)

	destDir := filepath.Join(root, "out.avi")
	dest, err := Resolve(src, destDir, s, "default", targetBytes, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest.Dir != destDir {
		t.Fatalf("expected dir %s, got %s", destDir, dest.Dir)
	}
	if filepath.Base(dest.Path) != "clip.fit20.mp4" {
		t.Fatalf("expected clip.fit20.mp4, got %s", dest.Path)
	}
}

func TestTrailingSeparatorTreatedAsDirectory(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "clip.mp4")
	s := config.Factory()
	targetBytes := int64(12500000)

	raw := filepath.Join(root, "out.mp4") + string(os.PathSeparator)
	dest, err := Resolve(src, raw, s, "default", targetBytes, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if filepath.Base(dest.Path) != "clip.fit12.5.mp4" {
		t.Fatalf("expected clip.fit12.5.mp4, got %s", dest.Path)
	}
}

func TestDirectoryAutoNameOrdinaryForceAndConversion(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "holiday.clip.mov")
	outDir := filepath.Join(root, "exports")

	s := config.Factory()
	targetBytes := int64(15000000)
	s.Codec = "av1"
	s.Container = "webm"

	ord, err := Resolve(src, outDir, s, "default", targetBytes, false)
	if err != nil {
		t.Fatalf("ordinary resolve failed: %v", err)
	}
	if filepath.Base(ord.Path) != "holiday.clip.fit15.webm" {
		t.Fatalf("unexpected ordinary autoname: %s", ord.Path)
	}

	ft, err := Resolve(src, outDir, s, "default", targetBytes, false)
	if err != nil {
		t.Fatalf("force-target resolve failed: %v", err)
	}
	if filepath.Base(ft.Path) != "holiday.clip.fit15.webm" {
		t.Fatalf("unexpected force-target autoname: %s", ft.Path)
	}

	conv, err := Resolve(src, outDir, s, "conversion", targetBytes, false)
	if err != nil {
		t.Fatalf("conversion resolve failed: %v", err)
	}
	if filepath.Base(conv.Path) != "holiday.clip.av1.webm" {
		t.Fatalf("unexpected conversion autoname: %s", conv.Path)
	}
}

func TestResolveReadOnlyDoesNotCreateDirectories(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "clip.mp4")
	absent := filepath.Join(root, "absent", "nested")

	dest, err := Resolve(src, filepath.Join(absent, "out.mp4"), config.Factory(), "default", int64(20000000), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dest.Dir != absent {
		t.Fatalf("expected dir %s, got %s", absent, dest.Dir)
	}
	if _, statErr := os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(statErr) {
		t.Fatalf("Resolve must not create destination directories")
	}
}

func TestExistingDestinationRequiresOverwrite(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.mp4")
	out := filepath.Join(root, "out.mp4")
	if err := os.WriteFile(src, []byte("src"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Resolve(src, out, config.Factory(), "default", int64(20000000), false); err == nil {
		t.Fatalf("expected error when destination exists without overwrite")
	}
	if _, err := Resolve(src, out, config.Factory(), "default", int64(20000000), true); err != nil {
		t.Fatalf("expected overwrite resolve to succeed, got %v", err)
	}
}

func TestRejectAliasSamePathAndHardlink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.mp4")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RejectAlias(src, src); err == nil {
		t.Fatalf("expected same-path alias rejection")
	}

	hardlink := filepath.Join(root, "alias.mp4")
	if err := os.Link(src, hardlink); err != nil {
		t.Skipf("os.Link unsupported in test environment: %v", err)
	}
	if err := RejectAlias(src, hardlink); err == nil {
		t.Fatalf("expected hardlink alias rejection")
	}
	if _, err := Resolve(src, hardlink, config.Factory(), "default", int64(20000000), true); err == nil {
		t.Fatalf("expected Resolve to reject source hardlink even with overwrite")
	}
}

func TestTempCreatesUniquePartFilesInDestDir(t *testing.T) {
	root := t.TempDir()
	destDir := filepath.Join(root, "newdir")
	dest := Destination{Path: filepath.Join(destDir, "out.mp4"), Dir: destDir, Container: "mp4"}

	p1, err := Temp(dest)
	if err != nil {
		t.Fatalf("Temp 1 failed: %v", err)
	}
	defer os.Remove(p1)

	p2, err := Temp(dest)
	if err != nil {
		t.Fatalf("Temp 2 failed: %v", err)
	}
	defer os.Remove(p2)

	if p1 == p2 {
		t.Fatalf("expected unique temp paths, both were %s", p1)
	}
	if filepath.Dir(p1) != destDir || !strings.HasSuffix(p1, ".part") {
		t.Fatalf("unexpected temp path %s", p1)
	}
}

func TestPublishNoOverwriteAndConcurrentCollision(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.mp4")
	out := filepath.Join(root, "out.mp4")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := Destination{Path: out, Dir: root, Container: "mp4"}

	part1, err := Temp(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part1, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(part1, dest, src, false); err != nil {
		t.Fatalf("initial no-overwrite publish failed: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "first" {
		t.Fatalf("unexpected published content: %q (%v)", got, err)
	}

	// Simulate destination appearing concurrently before publish without overwrite.
	part2, err := Temp(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(part2)
	if err := os.WriteFile(part2, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = Publish(part2, dest, src, false)
	if err == nil || !strings.Contains(err.Error(), "appeared") {
		t.Fatalf("expected concurrent collision error, got %v", err)
	}
	got, err = os.ReadFile(out)
	if err != nil || string(got) != "first" {
		t.Fatalf("existing destination was clobbered: %q (%v)", got, err)
	}
}

func TestPublishOverwriteReplacesExistingAndPreservesOnFailure(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.mp4")
	out := filepath.Join(root, "out.mp4")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := Destination{Path: out, Dir: root, Container: "mp4"}

	// Publishing a nonexistent part fails and leaves previous output intact.
	if err := Publish(filepath.Join(root, "missing.part"), dest, src, true); err == nil {
		t.Fatalf("expected error publishing missing part")
	}
	if got, _ := os.ReadFile(out); string(got) != "previous" {
		t.Fatalf("previous output should remain intact on failed publish, got %q", got)
	}

	part, err := Temp(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte("finished"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Publish(part, dest, src, true); err != nil {
		t.Fatalf("overwrite publish failed: %v", err)
	}
	if got, _ := os.ReadFile(out); string(got) != "finished" {
		t.Fatalf("expected finished content after overwrite, got %q", got)
	}
}

func TestPublishRefusesSourceAliasCreatedDuringEncode(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.mp4")
	out := filepath.Join(root, "out.mp4")
	if err := os.WriteFile(src, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := Destination{Path: out, Dir: root, Container: "mp4"}
	part, err := Temp(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(part)
	if err := os.WriteFile(part, []byte("encoded"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Hardlink created after Resolve, before Publish.
	if err := os.Link(src, out); err != nil {
		t.Skipf("os.Link unsupported: %v", err)
	}
	if err := Publish(part, dest, src, true); err == nil {
		t.Fatalf("expected Publish to refuse source alias created during encode")
	}
	if got, _ := os.ReadFile(src); string(got) != "source" {
		t.Fatalf("source file was modified: %q", got)
	}
}

func TestPOSIXLinkCollisionReportsDestinationAppeared(t *testing.T) {
	removed := false
	err := publishLinkNoOverwrite(
		"/tmp/a.part",
		"/tmp/out.mp4",
		func(string, string) error { return os.ErrExist },
		func(string) error { removed = true; return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "destination appeared during encoding") {
		t.Fatalf("expected destination appeared error, got %v", err)
	}
	if removed {
		t.Fatalf("temp file must not be unlinked when link fails")
	}

	err = publishLinkNoOverwrite(
		"/tmp/a.part",
		"/tmp/out.mp4",
		func(string, string) error { return nil },
		func(string) error { removed = true; return nil },
	)
	if err != nil || !removed {
		t.Fatalf("expected clean link+remove, got err=%v removed=%v", err, removed)
	}
}
