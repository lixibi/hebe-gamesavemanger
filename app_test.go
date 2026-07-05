package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStatusForGameDetectsCloudOnlyFiles(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "bg3",
		Name:          "Baldur's Gate 3",
		FolderName:    "bg3",
		LocalSavePath: filepath.Join(root, "local", "bg3"),
	}

	writeTestFile(t, filepath.Join(game.LocalSavePath, "profile", "save1.dat"), "old")
	writeTestFile(t, filepath.Join(app.cloudSavePath(game), "profile", "save1.dat"), "old")
	writeTestFile(t, filepath.Join(app.cloudSavePath(game), "profile", "save2.dat"), "new")

	status := app.statusForGame(game)
	if status.State != "cloud-newer" {
		t.Fatalf("expected cloud-newer, got %q: %+v", status.State, status)
	}
	if status.CloudOnly != 1 || status.LocalOnly != 0 || status.Changed != 0 {
		t.Fatalf("unexpected diff counts: %+v", status)
	}
	if status.LastChangeSide != "cloud" {
		t.Fatalf("expected cloud to be inferred newer, got %q", status.LastChangeSide)
	}
}

func TestCloudPathUsesGameFolderDirectly(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "bg3",
		Name:          "Baldur's Gate 3",
		FolderName:    "bg3",
		LocalSavePath: filepath.Join(root, "local", "bg3"),
		SaveSubdir:    "legacy-savedata",
	}

	if got, want := app.cloudSavePath(game), filepath.Join(root, "data", "bg3"); got != want {
		t.Fatalf("expected cloud path to use game folder directly, got %q want %q", got, want)
	}
}

func TestStatusForGameDetectsChangedFileNewerSide(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "changed",
		Name:          "Changed",
		FolderName:    "changed",
		LocalSavePath: filepath.Join(root, "local", "changed"),
	}

	localPath := filepath.Join(game.LocalSavePath, "slot.sav")
	cloudPath := filepath.Join(app.cloudSavePath(game), "slot.sav")
	writeTestFile(t, localPath, "old")
	writeTestFile(t, cloudPath, "new")
	oldTime := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	newTime := oldTime.Add(5 * time.Minute)
	if err := os.Chtimes(localPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cloudPath, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	status := app.statusForGame(game)
	if status.State != "cloud-newer" || status.LastChangeSide != "cloud" {
		t.Fatalf("expected cloud-newer, got state=%q side=%q reason=%q", status.State, status.LastChangeSide, status.LastChangeReason)
	}
	if status.LastChangePath != "slot.sav" {
		t.Fatalf("expected reason path to identify changed file, got %q", status.LastChangePath)
	}
}

func TestStatusForGameTreatsBothSidesChangingAsConflict(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "conflict",
		Name:          "Conflict",
		FolderName:    "conflict",
		LocalSavePath: filepath.Join(root, "local", "conflict"),
	}

	writeTestFile(t, filepath.Join(game.LocalSavePath, "local-only.sav"), "local")
	writeTestFile(t, filepath.Join(app.cloudSavePath(game), "cloud-only.sav"), "cloud")

	status := app.statusForGame(game)
	if status.State != "conflict" || status.LastChangeSide != "both" {
		t.Fatalf("expected conflict with both sides changing, got state=%q side=%q reason=%q", status.State, status.LastChangeSide, status.LastChangeReason)
	}
}

func TestStatusForGameScansAllNestedFileFormats(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "mixed",
		Name:          "Mixed Saves",
		FolderName:    "mixed",
		LocalSavePath: filepath.Join(root, "local", "mixed"),
	}

	paths := []string{
		"slot1.sav",
		"profile.dat",
		"screenshot.png",
		"metadata",
		".hidden",
		filepath.Join("nested", "deep", "state.bin"),
		filepath.Join("nested", "deep", "notes.json"),
	}
	for _, rel := range paths {
		writeTestFile(t, filepath.Join(game.LocalSavePath, rel), rel)
		writeTestFile(t, filepath.Join(app.cloudSavePath(game), rel), rel)
	}

	status := app.statusForGame(game)
	if status.State != "in-sync" {
		t.Fatalf("expected in-sync, got %q: %+v", status.State, status)
	}
	if status.LocalFiles != len(paths) || status.CloudFiles != len(paths) {
		t.Fatalf("expected every nested file format to be scanned, got local=%d cloud=%d", status.LocalFiles, status.CloudFiles)
	}
}

func TestSyncGameBacksUpDestinationBeforeOverwrite(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "bg3",
		Name:          "Baldur's Gate 3",
		FolderName:    "bg3",
		LocalSavePath: filepath.Join(root, "local", "bg3"),
	}

	if _, err := app.SaveGame(game); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.dat"), "local")
	writeTestFile(t, filepath.Join(app.cloudSavePath(game), "slot.dat"), "cloud")

	result, err := app.SyncGame("bg3", "cloud-to-local")
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected backup path")
	}
	if filepath.Dir(result.BackupPath) != app.gameBackupDir("bg3") {
		t.Fatalf("expected backup to be grouped by game, got %q", result.BackupPath)
	}
	if got := readTestFile(t, filepath.Join(game.LocalSavePath, "slot.dat")); got != "cloud" {
		t.Fatalf("expected local save to be overwritten from cloud, got %q", got)
	}
	if got := readTestFile(t, filepath.Join(result.BackupPath, "slot.dat")); got != "local" {
		t.Fatalf("expected backup to keep original local save, got %q", got)
	}
}

func TestSaveGameAllowsEmptyLocalDirectoryWithoutInitialUpload(t *testing.T) {
	root := t.TempDir()
	configRequests := 0
	uploadRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/games":
			_ = json.NewEncoder(w).Encode(cloudGamesResponse{Games: []cloudGameConfig{{
				ID:         "empty",
				Name:       "Empty",
				FolderName: "empty",
			}}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/games/empty/config":
			configRequests++
			_ = json.NewEncoder(w).Encode(cloudGameConfig{ID: "empty", Name: "Empty", FolderName: "empty"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/games/empty/manifest":
			_ = json.NewEncoder(w).Encode(cloudManifestResponse{
				Files: map[string]struct {
					Hash    string `json:"hash"`
					Size    int64  `json:"size"`
					ModTime string `json:"modTime"`
				}{},
				Dirs: []string{},
			})
		case (r.Method == http.MethodPut || r.Method == http.MethodPost) && r.URL.Path == "/api/games/empty/archive":
			uploadRequests++
			http.Error(w, "empty upload should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	app := newTestApp(t, root)
	if err := app.saveConfig(Config{CloudServerURL: server.URL, CloudPassword: "hebesave", Games: []GameConfig{}}); err != nil {
		t.Fatal(err)
	}
	game := GameConfig{
		ID:            "empty",
		Name:          "Empty",
		FolderName:    "empty",
		LocalSavePath: filepath.Join(root, "local", "empty"),
	}
	if err := os.MkdirAll(game.LocalSavePath, 0o755); err != nil {
		t.Fatal(err)
	}

	state, err := app.SaveGame(game)
	if err != nil {
		t.Fatal(err)
	}
	if configRequests != 1 {
		t.Fatalf("expected cloud config to be saved once, got %d", configRequests)
	}
	if uploadRequests != 0 {
		t.Fatalf("expected empty local directory to skip initial upload, got %d uploads", uploadRequests)
	}
	if len(state.Games) != 1 || state.Games[0].LocalFiles != 0 {
		t.Fatalf("expected saved empty game state, got %+v", state.Games)
	}
}

func TestSyncGameKeepsLatestFiveBackupsPerGame(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "bg3",
		Name:          "Baldur's Gate 3",
		FolderName:    "bg3",
		LocalSavePath: filepath.Join(root, "local", "bg3"),
	}

	if _, err := app.SaveGame(game); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(app.cloudSavePath(game), "slot.dat"), "cloud")

	for i := 0; i < 7; i++ {
		writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.dat"), string(rune('a'+i)))
		if _, err := app.SyncGame("bg3", "cloud-to-local"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	entries, err := os.ReadDir(app.gameBackupDir("bg3"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	if count != maxBackupsPerGame {
		t.Fatalf("expected %d backups, got %d", maxBackupsPerGame, count)
	}
}

func TestManualBackupAndRestoreBackup(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "restore",
		Name:          "Restore",
		FolderName:    "restore",
		LocalSavePath: filepath.Join(root, "local", "restore"),
	}

	if _, err := app.SaveGame(game); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.sav"), "backup-version")

	backup, err := app.CreateManualBackup("restore")
	if err != nil {
		t.Fatal(err)
	}
	if backup.Name == "" || backup.Files != 1 {
		t.Fatalf("unexpected backup info: %+v", backup)
	}

	writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.sav"), "current-version")
	result, err := app.RestoreBackup("restore", backup.Name)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupPath == "" {
		t.Fatal("expected current local save to be backed up before restore")
	}
	if got := readTestFile(t, filepath.Join(game.LocalSavePath, "slot.sav")); got != "backup-version" {
		t.Fatalf("expected backup to be restored to local save path, got %q", got)
	}
}

func TestAutoUploadBacksUpCloudOnlyOncePerSession(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "auto",
		Name:          "Auto",
		FolderName:    "auto",
		LocalSavePath: filepath.Join(root, "local", "auto"),
	}
	session := &autoUploadSession{game: game}

	writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.sav"), "local-1")
	writeTestFile(t, filepath.Join(app.cloudSavePath(game), "slot.sav"), "cloud")
	localTime := time.Now().Add(5 * time.Minute)
	if err := os.Chtimes(filepath.Join(game.LocalSavePath, "slot.sav"), localTime, localTime); err != nil {
		t.Fatal(err)
	}

	if err := app.autoUploadIfLocalNewer(session); err != nil {
		t.Fatal(err)
	}
	if !session.cloudBackedUp {
		t.Fatal("expected first auto upload to back up cloud")
	}
	if got := readTestFile(t, filepath.Join(app.cloudSavePath(game), "slot.sav")); got != "local-1" {
		t.Fatalf("expected cloud to receive local save, got %q", got)
	}

	writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.sav"), "local-2")
	nextTime := time.Now().Add(10 * time.Minute)
	if err := os.Chtimes(filepath.Join(game.LocalSavePath, "slot.sav"), nextTime, nextTime); err != nil {
		t.Fatal(err)
	}
	if err := app.autoUploadIfLocalNewer(session); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(app.gameBackupDir("auto"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one cloud backup for auto upload session, got %d", count)
	}
}

func TestLocalSessionPersistsFileChangeAcrossRestart(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "track",
		Name:          "Track",
		FolderName:    "track",
		LocalSavePath: filepath.Join(root, "local", "track"),
	}
	writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.sav"), "before")

	session, err := app.newAutoUploadSession(game)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(game.LocalSavePath, "slot.sav"), "after")
	if err := app.observeLocalSessionChange(session); err != nil {
		t.Fatal(err)
	}
	if !session.localChanged {
		t.Fatal("expected local file change to be detected")
	}

	restarted := newTestApp(t, root)
	state, err := restarted.loadLocalSessionState(game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.LocalChanged {
		t.Fatal("expected local change state to survive app restart")
	}
	resumed, err := restarted.newAutoUploadSession(game)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.localChanged {
		t.Fatal("expected resumed session to keep pending local change")
	}
}

func TestStatusUsesTrackedLocalChangeWhenModTimeIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "tracked-status",
		Name:          "Tracked Status",
		FolderName:    "tracked-status",
		LocalSavePath: filepath.Join(root, "local", "tracked-status"),
	}
	localPath := filepath.Join(game.LocalSavePath, "slot.sav")
	cloudPath := filepath.Join(app.cloudSavePath(game), "slot.sav")
	writeTestFile(t, localPath, "before")
	session, err := app.newAutoUploadSession(game)
	if err != nil {
		t.Fatal(err)
	}

	writeTestFile(t, localPath, "local-after")
	writeTestFile(t, cloudPath, "cloud-other")
	sameTime := time.Now()
	if err := os.Chtimes(localPath, sameTime, sameTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cloudPath, sameTime, sameTime); err != nil {
		t.Fatal(err)
	}
	if err := app.observeLocalSessionChange(session); err != nil {
		t.Fatal(err)
	}

	status := app.statusForGame(game)
	if status.LastChangeSide != "local" {
		t.Fatalf("expected tracked local change to break ambiguous diff, got %+v", status)
	}
}

func TestSyncGamePreservesEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	game := GameConfig{
		ID:            "empty-dirs",
		Name:          "Empty Dirs",
		FolderName:    "empty-dirs",
		LocalSavePath: filepath.Join(root, "local", "empty-dirs"),
	}

	if err := os.MkdirAll(filepath.Join(game.LocalSavePath, "old-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app.cloudSavePath(game), "new-empty", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(app.cloudSavePath(game), "profile", "save.anything"), "cloud")

	if _, err := app.SaveGame(game); err != nil {
		t.Fatal(err)
	}
	result, err := app.SyncGame("empty-dirs", "cloud-to-local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(game.LocalSavePath, "new-empty", "nested")); err != nil {
		t.Fatalf("expected empty cloud directory to be restored locally: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.BackupPath, "old-empty")); err != nil {
		t.Fatalf("expected empty local directory to be preserved in backup: %v", err)
	}
}

func TestSnapshotVerificationDetectsEmptyDirectoryMismatch(t *testing.T) {
	left := directorySnapshot{
		Files: map[string]fileInfo{},
		Dirs:  map[string]struct{}{"empty-slot": {}},
	}
	right := directorySnapshot{
		Files: map[string]fileInfo{},
		Dirs:  map[string]struct{}{},
	}
	if err := verifySnapshotsEqual(left, right, "left", "right"); err == nil {
		t.Fatal("expected empty directory mismatch to be detected")
	}
}

func TestSyncGameRejectsNestedSourceAndDestination(t *testing.T) {
	root := t.TempDir()
	app := newTestApp(t, root)
	src := filepath.Join(root, "save")
	dst := filepath.Join(src, "nested")
	writeTestFile(t, filepath.Join(src, "slot.sav"), "save")

	if _, err := app.replaceDirectory(dst, src, "bad", "local-to-cloud"); err == nil {
		t.Fatal("expected nested source and destination to be rejected")
	}
}

func TestSteamLaunchTargetIsRecognizedAsURL(t *testing.T) {
	if !isURLTarget("steam://rungameid/1086940") {
		t.Fatal("expected steam launch target to be recognized as URL")
	}
	if isURLTarget(filepath.Join("Games", "Game", "game.exe")) {
		t.Fatal("expected normal executable path not to be recognized as URL")
	}
}

func writeTestFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func newTestApp(t *testing.T, root string) *App {
	t.Helper()
	app := newAppAt(root)
	if err := app.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.saveConfig(Config{CloudServerURL: "local", Games: []GameConfig{}}); err != nil {
		t.Fatal(err)
	}
	return app
}
