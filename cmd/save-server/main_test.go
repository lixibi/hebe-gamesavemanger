package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceGameDirSkipsBackupForEmptyTarget(t *testing.T) {
	root := t.TempDir()
	s := &server{
		root:      root,
		backupDir: filepath.Join(root, ".backups"),
	}
	game := "smoke"
	if err := os.MkdirAll(s.gameDir(game), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "src")
	writeTestFile(t, src, "save.sav", "first")

	backup, err := s.replaceGameDir(game, src, "upload")
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Fatalf("expected no backup for empty target, got %q", backup)
	}
	backups, err := s.listBackups(game)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected no backups after first upload, got %d", len(backups))
	}
}

func TestReplaceGameDirPrunesToFiveNonEmptyBackups(t *testing.T) {
	root := t.TempDir()
	s := &server{
		root:      root,
		backupDir: filepath.Join(root, ".backups"),
	}
	game := "smoke"
	src := filepath.Join(root, "src")
	for i := range 7 {
		writeTestFile(t, src, "save.sav", string(rune('a'+i)))
		if _, err := s.replaceGameDir(game, src, "upload"); err != nil {
			t.Fatal(err)
		}
	}

	backups, err := s.listBackups(game)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != maxBackupsPerGame {
		t.Fatalf("expected %d backups, got %d", maxBackupsPerGame, len(backups))
	}
	for _, backup := range backups {
		if backup.Files == 0 {
			t.Fatalf("expected only non-empty backups, got %+v", backup)
		}
	}
}

func TestReplaceGameDirCanRestoreOldestKeptBackup(t *testing.T) {
	root := t.TempDir()
	s := &server{
		root:      root,
		backupDir: filepath.Join(root, ".backups"),
	}
	game := "smoke"
	src := filepath.Join(root, "src")
	for i := range 7 {
		writeTestFile(t, src, "save.sav", string(rune('a'+i)))
		if _, err := s.replaceGameDir(game, src, "upload"); err != nil {
			t.Fatal(err)
		}
	}

	backups, err := s.listBackups(game)
	if err != nil {
		t.Fatal(err)
	}
	oldestKept := backups[len(backups)-1].Name
	if _, err := s.replaceGameDir(game, filepath.Join(s.gameBackupDir(game), oldestKept), "restore"); err != nil {
		t.Fatalf("restore oldest kept backup: %v", err)
	}
}

func writeTestFile(t *testing.T, root string, rel string, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
