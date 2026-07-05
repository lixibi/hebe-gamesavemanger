package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	maxBackupsPerGame     = 5
	defaultCloudServerURL = "http://127.0.0.1:27843"
)

type App struct {
	ctx           context.Context
	rootDir       string
	configPath    string
	dataDir       string
	backupDir     string
	autoSessions  map[string]chan struct{}
	syncthingCmd  *exec.Cmd
	syncthingLock sync.Mutex
	autoLock      sync.Mutex
}

type Config struct {
	CloudServerURL string       `json:"cloudServerURL"`
	Games          []GameConfig `json:"games"`
}

type GameConfig struct {
	ID                        string `json:"id"`
	Name                      string `json:"name"`
	FolderName                string `json:"folderName"`
	LocalSavePath             string `json:"localSavePath"`
	GameExePath               string `json:"gameExePath"`
	GameArgs                  string `json:"gameArgs"`
	AutoUploadMode            string `json:"autoUploadMode"`
	AutoUploadIntervalMinutes int    `json:"autoUploadIntervalMinutes"`
	SaveSubdir                string `json:"saveSubdir,omitempty"`
}

type AppState struct {
	RootDir          string       `json:"rootDir"`
	ConfigPath       string       `json:"configPath"`
	DataDir          string       `json:"dataDir"`
	CloudServerURL   string       `json:"cloudServerURL"`
	SyncthingStatus  string       `json:"syncthingStatus"`
	SyncthingMessage string       `json:"syncthingMessage"`
	Games            []GameStatus `json:"games"`
}

type GameStatus struct {
	Game             GameConfig `json:"game"`
	CloudPath        string     `json:"cloudPath"`
	State            string     `json:"state"`
	Message          string     `json:"message"`
	LastChangeSide   string     `json:"lastChangeSide"`
	LastChangeReason string     `json:"lastChangeReason"`
	LastChangePath   string     `json:"lastChangePath"`
	LastCheckedAt    string     `json:"lastCheckedAt"`
	LocalFiles       int        `json:"localFiles"`
	CloudFiles       int        `json:"cloudFiles"`
	LocalBytes       int64      `json:"localBytes"`
	CloudBytes       int64      `json:"cloudBytes"`
	LocalOnly        int        `json:"localOnly"`
	CloudOnly        int        `json:"cloudOnly"`
	Changed          int        `json:"changed"`
	LocalModified    string     `json:"localModified"`
	CloudModified    string     `json:"cloudModified"`
	LocalLatestPath  string     `json:"localLatestPath"`
	CloudLatestPath  string     `json:"cloudLatestPath"`
}

type SyncResult struct {
	BackupPath string     `json:"backupPath"`
	Status     GameStatus `json:"status"`
}

type BackupInfo struct {
	Name           string `json:"name"`
	Path           string `json:"path"`
	CreatedAt      string `json:"createdAt"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	LatestModified string `json:"latestModified"`
	LatestPath     string `json:"latestPath"`
}

type fileInfo struct {
	Hash    string
	Size    int64
	ModTime time.Time
}

type directorySnapshot struct {
	Files map[string]fileInfo
	Dirs  map[string]struct{}
}

type diffResult struct {
	LocalOnly  int
	CloudOnly  int
	Changed    int
	NewerSide  string
	Reason     string
	ReasonPath string
}

type freshnessEvidence struct {
	Side   string
	Reason string
	Path   string
	Time   time.Time
}

type autoUploadSession struct {
	game          GameConfig
	cloudBackedUp bool
}

func NewApp() *App {
	root := discoverRootDir()
	return newAppAt(root)
}

func newAppAt(root string) *App {
	return &App{
		rootDir:      root,
		configPath:   filepath.Join(root, "config", "games.json"),
		dataDir:      filepath.Join(root, "data"),
		backupDir:    filepath.Join(root, "backups"),
		autoSessions: map[string]chan struct{}{},
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	_ = a.ensureLayout()
}

func (a *App) GetAppState() (AppState, error) {
	if err := a.ensureLayout(); err != nil {
		return AppState{}, err
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return AppState{}, err
	}

	statuses := make([]GameStatus, 0, len(cfg.Games))
	for _, game := range cfg.Games {
		statuses = append(statuses, a.statusForGame(game))
	}

	syncStatus, syncMessage := a.cloudServerStatus(cfg.CloudServerURL)
	return AppState{
		RootDir:          a.rootDir,
		ConfigPath:       a.configPath,
		DataDir:          a.dataDir,
		CloudServerURL:   cfg.CloudServerURL,
		SyncthingStatus:  syncStatus,
		SyncthingMessage: syncMessage,
		Games:            statuses,
	}, nil
}

func (a *App) SaveGame(game GameConfig) (AppState, error) {
	if err := a.ensureLayout(); err != nil {
		return AppState{}, err
	}
	if err := game.normalizeAndValidate(); err != nil {
		return AppState{}, err
	}

	cfg, err := a.loadConfig()
	if err != nil {
		return AppState{}, err
	}

	replaced := false
	for i := range cfg.Games {
		if cfg.Games[i].ID == game.ID {
			cfg.Games[i] = game
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Games = append(cfg.Games, game)
	}
	sort.Slice(cfg.Games, func(i, j int) bool {
		return strings.ToLower(cfg.Games[i].Name) < strings.ToLower(cfg.Games[j].Name)
	})

	if err := a.saveConfig(cfg); err != nil {
		return AppState{}, err
	}
	_ = os.MkdirAll(a.cloudSavePath(game), 0o755)
	return a.GetAppState()
}

func (a *App) DeleteGame(id string) (AppState, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return AppState{}, err
	}

	next := cfg.Games[:0]
	for _, game := range cfg.Games {
		if game.ID != id {
			next = append(next, game)
		}
	}
	cfg.Games = next

	if err := a.saveConfig(cfg); err != nil {
		return AppState{}, err
	}
	return a.GetAppState()
}

func (a *App) SyncGame(id string, direction string) (SyncResult, error) {
	game, err := a.findGame(id)
	if err != nil {
		return SyncResult{}, err
	}

	if a.cloudBaseURL() == "local" {
		var src, dst string
		switch direction {
		case "cloud-to-local":
			src = a.cloudSavePath(game)
			dst = game.LocalSavePath
		case "local-to-cloud":
			src = game.LocalSavePath
			dst = a.cloudSavePath(game)
		default:
			return SyncResult{}, fmt.Errorf("unknown sync direction: %s", direction)
		}
		backup, err := a.replaceDirectory(dst, src, id, direction)
		if err != nil {
			return SyncResult{}, err
		}
		return SyncResult{BackupPath: backup, Status: a.statusForGame(game)}, nil
	}

	switch direction {
	case "cloud-to-local":
		stage, cleanup, err := a.downloadCloudToTemp(game)
		if err != nil {
			return SyncResult{}, err
		}
		defer cleanup()
		backup, err := a.replaceDirectory(game.LocalSavePath, stage, id, direction)
		if err != nil {
			return SyncResult{}, err
		}
		return SyncResult{BackupPath: backup, Status: a.statusForGame(game)}, nil
	case "local-to-cloud":
		backup, err := a.uploadLocalToCloud(game, true)
		if err != nil {
			return SyncResult{}, err
		}
		return SyncResult{BackupPath: backup, Status: a.statusForGame(game)}, nil
	default:
		return SyncResult{}, fmt.Errorf("unknown sync direction: %s", direction)
	}
}

func (a *App) LaunchGame(id string) error {
	game, err := a.findGame(id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(game.GameExePath) == "" {
		return errors.New("game executable path is empty")
	}

	cmd := launchCommandWithArgs(game.GameExePath, game.GameArgs)
	if err := cmd.Start(); err != nil {
		return err
	}
	a.startAutoUploadSession(game, cmd)
	return nil
}

func (a *App) StartSyncthing() (AppState, error) {
	return a.GetAppState()
}

func (a *App) ExportGameConfig(id string) (string, error) {
	game, err := a.findGame(id)
	if err != nil {
		return "", err
	}
	game.applyDefaults()
	defaultName := safeName(game.Name)
	if defaultName == "" {
		defaultName = game.ID
	}
	path, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:                "导出游戏配置",
		DefaultFilename:      defaultName + ".hebe-game.json",
		CanCreateDirectories: true,
		Filters: []wailsruntime.FileFilter{{
			DisplayName: "JSON 配置 (*.json)",
			Pattern:     "*.json",
		}},
	})
	if err != nil || path == "" {
		return "", err
	}
	raw, err := json.MarshalIndent(game, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) ImportGameConfig() (GameConfig, error) {
	path, err := wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title: "导入游戏配置",
		Filters: []wailsruntime.FileFilter{{
			DisplayName: "JSON 配置 (*.json)",
			Pattern:     "*.json",
		}},
	})
	if err != nil || path == "" {
		return GameConfig{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return GameConfig{}, err
	}
	var game GameConfig
	if err := json.Unmarshal(raw, &game); err != nil {
		return GameConfig{}, err
	}
	game.applyDefaults()
	return game, nil
}

func (a *App) PickGameExe() (string, error) {
	return wailsruntime.OpenFileDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:           "选择游戏程序",
		ShowHiddenFiles: true,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "可执行文件 (*.exe)", Pattern: "*.exe"},
			{DisplayName: "快捷方式 (*.lnk)", Pattern: "*.lnk"},
			{DisplayName: "所有文件 (*.*)", Pattern: "*.*"},
		},
	})
}

func (a *App) PickSaveDirectory() (string, error) {
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:           "选择存档目录",
		ShowHiddenFiles: true,
	})
}
func (a *App) OpenGamePath(id string, target string) error {
	game, err := a.findGame(id)
	if err != nil {
		return err
	}

	var path string
	switch target {
	case "local":
		path = game.LocalSavePath
	case "cloud":
		path = a.cloudSavePath(game)
	case "game":
		if strings.TrimSpace(game.GameExePath) == "" {
			return errors.New("game executable path is empty")
		}
		path = filepath.Dir(game.GameExePath)
	default:
		return fmt.Errorf("unknown path target: %s", target)
	}
	return openPath(path)
}

func (a *App) CreateManualBackup(id string) (BackupInfo, error) {
	game, err := a.findGame(id)
	if err != nil {
		return BackupInfo{}, err
	}
	if info, err := os.Stat(game.LocalSavePath); err != nil {
		return BackupInfo{}, err
	} else if !info.IsDir() {
		return BackupInfo{}, fmt.Errorf("local save path is not a directory: %s", game.LocalSavePath)
	}

	backupPath := filepath.Join(a.gameBackupDir(id), backupName("manual"))
	if err := copyDirectoryVerified(game.LocalSavePath, backupPath); err != nil {
		return BackupInfo{}, err
	}
	if err := a.pruneBackups(id); err != nil {
		return BackupInfo{}, err
	}
	return backupInfo(backupPath)
}

func (a *App) ListBackups(id string) ([]BackupInfo, error) {
	if _, err := a.findGame(id); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(a.gameBackupDir(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []BackupInfo{}, nil
		}
		return nil, err
	}

	backups := []BackupInfo{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := backupInfo(filepath.Join(a.gameBackupDir(id), entry.Name()))
		if err != nil {
			return nil, err
		}
		backups = append(backups, info)
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt > backups[j].CreatedAt
	})
	if len(backups) > maxBackupsPerGame {
		backups = backups[:maxBackupsPerGame]
	}
	return backups, nil
}

func (a *App) RestoreBackup(id string, backupName string) (SyncResult, error) {
	game, err := a.findGame(id)
	if err != nil {
		return SyncResult{}, err
	}
	if backupName != filepath.Base(backupName) || strings.TrimSpace(backupName) == "" {
		return SyncResult{}, errors.New("invalid backup name")
	}
	backupPath := filepath.Join(a.gameBackupDir(id), backupName)
	if info, err := os.Stat(backupPath); err != nil {
		return SyncResult{}, err
	} else if !info.IsDir() {
		return SyncResult{}, fmt.Errorf("backup is not a directory: %s", backupName)
	}

	currentBackup, err := a.replaceDirectory(game.LocalSavePath, backupPath, id, "restore")
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		BackupPath: currentBackup,
		Status:     a.statusForGame(game),
	}, nil
}

func (a *App) ensureLayout() error {
	for _, dir := range []string{
		filepath.Dir(a.configPath),
		a.dataDir,
		a.backupDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if _, err := os.Stat(a.configPath); errors.Is(err, os.ErrNotExist) {
		return a.saveConfig(Config{CloudServerURL: defaultCloudServerURL, Games: []GameConfig{}})
	}
	return nil
}

func (a *App) loadConfig() (Config, error) {
	if err := a.ensureLayout(); err != nil {
		return Config{}, err
	}

	raw, err := os.ReadFile(a.configPath)
	if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return Config{CloudServerURL: defaultCloudServerURL, Games: []GameConfig{}}, nil
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.CloudServerURL) == "" {
		cfg.CloudServerURL = defaultCloudServerURL
	}
	for i := range cfg.Games {
		cfg.Games[i].applyDefaults()
	}
	return cfg, nil
}

func (a *App) saveConfig(cfg Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.configPath, append(raw, '\n'), 0o644)
}

func (a *App) findGame(id string) (GameConfig, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return GameConfig{}, err
	}
	for _, game := range cfg.Games {
		if game.ID == id {
			return game, nil
		}
	}
	return GameConfig{}, fmt.Errorf("game not found: %s", id)
}

func (a *App) statusForGame(game GameConfig) GameStatus {
	game.applyDefaults()
	status := GameStatus{
		Game:          game,
		CloudPath:     a.cloudSavePath(game),
		LastCheckedAt: time.Now().Format(time.RFC3339),
	}

	localManifest, localErr := scanDirectory(game.LocalSavePath)
	cloudManifest, cloudErr := a.cloudManifest(game)

	if localErr != nil {
		status.State = "missing-local"
		status.Message = localErr.Error()
	}
	if cloudErr != nil {
		if status.State == "" {
			status.State = "missing-cloud"
		}
		status.Message = cloudErr.Error()
	}
	if localErr != nil || cloudErr != nil {
		return status
	}

	status.LocalFiles, status.LocalBytes, status.LocalModified, status.LocalLatestPath = manifestStats(localManifest)
	status.CloudFiles, status.CloudBytes, status.CloudModified, status.CloudLatestPath = manifestStats(cloudManifest)
	diff := compareManifests(localManifest, cloudManifest)
	status.LocalOnly = diff.LocalOnly
	status.CloudOnly = diff.CloudOnly
	status.Changed = diff.Changed
	status.LastChangeSide = diff.NewerSide
	status.LastChangeReason = diff.Reason
	status.LastChangePath = diff.ReasonPath
	status.State, status.Message = describeDiff(status)
	return status
}

func (a *App) cloudSavePath(game GameConfig) string {
	game.applyDefaults()
	return filepath.Join(a.dataDir, game.FolderName)
}

type cloudManifestResponse struct {
	Files map[string]struct {
		Hash    string `json:"hash"`
		Size    int64  `json:"size"`
		ModTime string `json:"modTime"`
	} `json:"files"`
}

type cloudUploadResponse struct {
	Backup string `json:"backup"`
}

func (a *App) cloudBaseURL() string {
	cfg, err := a.loadConfig()
	if err != nil || strings.TrimSpace(cfg.CloudServerURL) == "" {
		return defaultCloudServerURL
	}
	return strings.TrimRight(cfg.CloudServerURL, "/")
}

func (a *App) cloudGameURL(game GameConfig, suffix string) string {
	game.applyDefaults()
	return a.cloudBaseURL() + "/api/games/" + url.PathEscape(game.FolderName) + suffix
}

func (a *App) cloudManifest(game GameConfig) (map[string]fileInfo, error) {
	if a.cloudBaseURL() == "local" {
		return scanDirectory(a.cloudSavePath(game))
	}
	req, err := http.NewRequest(http.MethodGet, a.cloudGameURL(game, "/manifest"), nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	var payload cloudManifestResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	files := map[string]fileInfo{}
	for path, file := range payload.Files {
		modTime, err := time.Parse(time.RFC3339Nano, file.ModTime)
		if err != nil {
			modTime, _ = time.Parse(time.RFC3339, file.ModTime)
		}
		files[path] = fileInfo{
			Hash:    file.Hash,
			Size:    file.Size,
			ModTime: modTime,
		}
	}
	return files, nil
}

func (a *App) downloadCloudToTemp(game GameConfig) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "hebe-cloud-download-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	if a.cloudBaseURL() == "local" {
		if err := copyDirectoryVerified(a.cloudSavePath(game), tmp); err != nil {
			cleanup()
			return "", cleanup, err
		}
		return tmp, cleanup, nil
	}
	req, err := http.NewRequest(http.MethodGet, a.cloudGameURL(game, "/archive"), nil)
	if err != nil {
		cleanup()
		return "", cleanup, err
	}
	client := http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		cleanup()
		return "", cleanup, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cleanup()
		return "", cleanup, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	if err := extractTarGz(resp.Body, tmp); err != nil {
		cleanup()
		return "", cleanup, err
	}
	return tmp, cleanup, nil
}

func (a *App) uploadLocalToCloud(game GameConfig, backup bool) (string, error) {
	if a.cloudBaseURL() == "local" {
		return a.replaceDirectoryWithBackup(a.cloudSavePath(game), game.LocalSavePath, game.ID, "auto-upload", backup)
	}
	reader, writer := io.Pipe()
	go func() {
		err := writeTarGz(writer, game.LocalSavePath)
		_ = writer.CloseWithError(err)
	}()
	req, err := http.NewRequest(http.MethodPut, a.cloudGameURL(game, "/archive"), reader)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/gzip")
	client := http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("cloud server returned %s", resp.Status)
	}
	var payload cloudUploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Backup == "" || !backup {
		return "", nil
	}
	return "cloud:" + payload.Backup, nil
}

func (a *App) cloudServerStatus(baseURL string) (string, string) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultCloudServerURL
	}
	if baseURL == "local" {
		return "running", "本地 data 兼容模式，仅用于测试或离线调试"
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return "stopped", err.Error()
	}
	client := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return "stopped", fmt.Sprintf("云服务未连接：%s", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "stopped", fmt.Sprintf("云服务返回 %s", resp.Status)
	}
	return "running", "自建云存档服务已连接：" + strings.TrimRight(baseURL, "/")
}

func (a *App) replaceDirectory(dst string, src string, gameID string, direction string) (string, error) {
	return a.replaceDirectoryWithBackup(dst, src, gameID, direction, true)
}

func (a *App) replaceDirectoryWithBackup(dst string, src string, gameID string, direction string, keepBackup bool) (string, error) {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return "", errors.New("source and destination paths are required")
	}
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	if samePath(src, dst) {
		return "", errors.New("source and destination are the same directory")
	}
	if pathsNested(src, dst) || pathsNested(dst, src) {
		return "", errors.New("source and destination cannot be inside each other")
	}
	if info, err := os.Stat(src); err != nil {
		return "", err
	} else if !info.IsDir() {
		return "", fmt.Errorf("source is not a directory: %s", src)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}

	backup := ""
	dstExists := false
	if info, err := os.Stat(dst); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("destination is not a directory: %s", dst)
		}
		dstExists = true
		if keepBackup {
			backup = filepath.Join(a.gameBackupDir(gameID), backupName(direction))
			if err := copyDirectoryVerified(dst, backup); err != nil {
				return "", fmt.Errorf("backup destination before overwrite: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	stage := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".gsm-staging-"+time.Now().Format("20060102150405"))
	_ = os.RemoveAll(stage)
	if err := copyDirectoryVerified(src, stage); err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	replaceErr := func() error {
		if dstExists {
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
		}
		if err := os.Rename(stage, dst); err != nil {
			return err
		}
		return verifyDirectoriesEqual(src, dst)
	}()
	if replaceErr != nil {
		if backup != "" {
			_ = os.RemoveAll(dst)
			_ = copyDirectoryVerified(backup, dst)
		}
		return "", replaceErr
	}
	if backup != "" {
		_ = a.pruneBackups(gameID)
	}

	return backup, nil
}

func (a *App) gameBackupDir(gameID string) string {
	return filepath.Join(a.backupDir, safeName(gameID))
}

func backupName(direction string) string {
	now := time.Now()
	return fmt.Sprintf("%s_%09d_%s", now.Format("20060102_150405"), now.Nanosecond(), direction)
}

func (a *App) pruneBackups(gameID string) error {
	backupDir := a.gameBackupDir(gameID)
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	type backupEntry struct {
		path    string
		modTime time.Time
	}
	backups := []backupEntry{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		backups = append(backups, backupEntry{
			path:    filepath.Join(backupDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].modTime.After(backups[j].modTime)
	})
	if len(backups) <= maxBackupsPerGame {
		return nil
	}
	for _, backup := range backups[maxBackupsPerGame:] {
		if err := os.RemoveAll(backup.path); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) startAutoUploadSession(game GameConfig, cmd *exec.Cmd) {
	status := a.statusForGame(game)
	if status.LastChangeSide == "cloud" || status.LastChangeSide == "both" || status.State == "missing-local" {
		go func() { _ = cmd.Wait() }()
		return
	}

	done := make(chan struct{})
	a.autoLock.Lock()
	if oldDone, ok := a.autoSessions[game.ID]; ok {
		close(oldDone)
	}
	a.autoSessions[game.ID] = done
	a.autoLock.Unlock()

	go func() {
		defer func() {
			a.autoLock.Lock()
			if a.autoSessions[game.ID] == done {
				delete(a.autoSessions, game.ID)
			}
			a.autoLock.Unlock()
		}()

		processDone := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(processDone)
		}()

		session := &autoUploadSession{game: game}
		interval := time.Duration(game.AutoUploadIntervalMinutes) * time.Minute
		if interval < time.Minute {
			interval = time.Minute
		}

		if game.AutoUploadMode == "interval" {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					_ = a.autoUploadIfLocalNewer(session)
				case <-processDone:
					_ = a.autoUploadIfLocalNewer(session)
					return
				}
			}
		}

		select {
		case <-done:
			return
		case <-processDone:
			if game.AutoUploadMode == "on-exit" {
				_ = a.autoUploadIfLocalNewer(session)
				return
			}
			if game.AutoUploadMode == "ask-on-exit" {
				a.promptUploadIfLocalNewer(game)
			}
		}
	}()
}

func (a *App) promptUploadIfLocalNewer(game GameConfig) {
	status := a.statusForGame(game)
	if status.LastChangeSide != "local" && status.State != "missing-cloud" {
		return
	}
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "game-local-newer-after-exit", status)
	}
}

func (a *App) autoUploadIfLocalNewer(session *autoUploadSession) error {
	status := a.statusForGame(session.game)
	if status.LastChangeSide != "local" && status.State != "missing-cloud" {
		return nil
	}
	backup := !session.cloudBackedUp
	if _, err := a.uploadLocalToCloud(session.game, backup); err != nil {
		return err
	}
	session.cloudBackedUp = true
	return nil
}

func backupInfo(path string) (BackupInfo, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return BackupInfo{}, err
	}
	files, bytes, latestModified, latestPath := 0, int64(0), "", ""
	if manifest, err := scanDirectory(path); err == nil {
		files, bytes, latestModified, latestPath = manifestStats(manifest)
	}
	return BackupInfo{
		Name:           filepath.Base(path),
		Path:           path,
		CreatedAt:      stat.ModTime().Format(time.RFC3339),
		Files:          files,
		Bytes:          bytes,
		LatestModified: latestModified,
		LatestPath:     latestPath,
	}, nil
}

func (a *App) startSyncthing() {
	a.syncthingLock.Lock()
	defer a.syncthingLock.Unlock()

	if a.syncthingCmd != nil && a.syncthingCmd.Process != nil {
		return
	}
	if isPortOpen("127.0.0.1:8384", 300*time.Millisecond) {
		return
	}

	binary := a.findSyncthingBinary()
	if binary == "" {
		return
	}

	args := []string{"-no-browser", "-no-restart"}
	if home := a.findSyncthingHome(); home != "" {
		args = append(args, "-home", home)
	}

	cmd := exec.Command(binary, args...)
	cmd.Dir = a.rootDir
	hideCommandWindow(cmd)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return
	}
	a.syncthingCmd = cmd
	go func() {
		_ = cmd.Wait()
		a.syncthingLock.Lock()
		if a.syncthingCmd == cmd {
			a.syncthingCmd = nil
		}
		a.syncthingLock.Unlock()
	}()
}

func (a *App) syncthingStatus() (string, string) {
	if isPortOpen("127.0.0.1:8384", 300*time.Millisecond) {
		return "running", "Syncthing default port 127.0.0.1:8384 is reachable"
	}
	if a.findSyncthingBinary() == "" {
		return "not-found", "Put syncthing or syncthing.exe next to this app"
	}
	return "stopped", "Syncthing binary was found but the GUI port is not reachable"
}

func (a *App) findSyncthingBinary() string {
	names := []string{"syncthing", filepath.Join("syncthing", "syncthing")}
	if runtime.GOOS == "windows" {
		names = []string{"syncthing.exe", filepath.Join("syncthing", "syncthing.exe")}
	}
	for _, dir := range candidateRootDirs(a.rootDir) {
		for _, name := range names {
			path := filepath.Join(dir, name)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path
			}
		}
	}
	return ""
}

func (a *App) findSyncthingHome() string {
	for _, root := range candidateRootDirs(a.rootDir) {
		for _, dir := range []string{
			filepath.Join(root, "syncthing-home"),
			filepath.Join(root, "syncthing", "config"),
			filepath.Join(root, "config", "syncthing"),
		} {
			if _, err := os.Stat(filepath.Join(dir, "config.xml")); err == nil {
				return dir
			}
		}
	}
	return ""
}

func (g *GameConfig) normalizeAndValidate() error {
	g.ID = safeName(strings.TrimSpace(g.ID))
	g.Name = strings.TrimSpace(g.Name)
	g.FolderName = safeName(strings.TrimSpace(g.FolderName))
	g.LocalSavePath = strings.TrimSpace(g.LocalSavePath)
	g.GameExePath = strings.TrimSpace(g.GameExePath)
	g.GameArgs = strings.TrimSpace(g.GameArgs)
	g.AutoUploadMode = strings.TrimSpace(g.AutoUploadMode)
	g.SaveSubdir = ""
	g.applyDefaults()

	if g.Name == "" {
		return errors.New("game name is required")
	}
	if g.FolderName == "" {
		return errors.New("cloud folder name is required")
	}
	if g.LocalSavePath == "" {
		return errors.New("local save path is required")
	}
	if g.ID == "" {
		g.ID = g.FolderName
	}
	return nil
}

func (g *GameConfig) applyDefaults() {
	if strings.TrimSpace(g.ID) == "" {
		g.ID = safeName(g.FolderName)
	}
	switch g.AutoUploadMode {
	case "", "off", "manual", "ask-on-exit", "interval", "on-exit":
	default:
		g.AutoUploadMode = "manual"
	}
	if g.AutoUploadMode == "" || g.AutoUploadMode == "off" {
		g.AutoUploadMode = "manual"
	}
	if g.AutoUploadIntervalMinutes < 1 {
		g.AutoUploadIntervalMinutes = 5
	}
	g.SaveSubdir = ""
}

func discoverRootDir() string {
	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		if _, err := os.Stat(filepath.Join(cwd, "wails.json")); err == nil {
			return cwd
		}
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Dir(exe)
	}
	if cwdErr == nil {
		return cwd
	}
	return "."
}

func scanDirectory(root string) (map[string]fileInfo, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("path is empty")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", root)
	}

	files := map[string]fileInfo{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = fileInfo{
			Hash:    hash,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		return nil
	})
	return files, err
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func manifestStats(manifest map[string]fileInfo) (int, int64, string, string) {
	var bytes int64
	var latest time.Time
	latestPath := ""
	for path, file := range manifest {
		bytes += file.Size
		if file.ModTime.After(latest) || (file.ModTime.Equal(latest) && (latestPath == "" || path < latestPath)) {
			latest = file.ModTime
			latestPath = path
		}
	}
	if latest.IsZero() {
		return len(manifest), bytes, "", ""
	}
	return len(manifest), bytes, latest.Format(time.RFC3339), latestPath
}

func compareManifests(local map[string]fileInfo, cloud map[string]fileInfo) diffResult {
	const modTimeTolerance = 2 * time.Second

	result := diffResult{}
	localSignals := 0
	cloudSignals := 0
	var localEvidence freshnessEvidence
	var cloudEvidence freshnessEvidence

	recordEvidence := func(evidence *freshnessEvidence, side string, reason string, path string, t time.Time) {
		if evidence.Time.IsZero() || t.After(evidence.Time) {
			*evidence = freshnessEvidence{
				Side:   side,
				Reason: reason,
				Path:   path,
				Time:   t,
			}
		}
	}

	for path, localFile := range local {
		cloudFile, ok := cloud[path]
		if !ok {
			result.LocalOnly++
			localSignals++
			recordEvidence(&localEvidence, "local", "本地有云端不存在的新文件", path, localFile.ModTime)
			continue
		}
		if localFile.Hash != cloudFile.Hash {
			result.Changed++
			if localFile.ModTime.After(cloudFile.ModTime.Add(modTimeTolerance)) {
				localSignals++
				recordEvidence(&localEvidence, "local", "同名文件内容不同，且本地修改时间更新", path, localFile.ModTime)
			} else if cloudFile.ModTime.After(localFile.ModTime.Add(modTimeTolerance)) {
				cloudSignals++
				recordEvidence(&cloudEvidence, "cloud", "同名文件内容不同，且云端修改时间更新", path, cloudFile.ModTime)
			}
		}
	}
	for path, cloudFile := range cloud {
		if _, ok := local[path]; !ok {
			result.CloudOnly++
			cloudSignals++
			recordEvidence(&cloudEvidence, "cloud", "云端有本地不存在的新文件", path, cloudFile.ModTime)
		}
	}

	switch {
	case result.LocalOnly == 0 && result.CloudOnly == 0 && result.Changed == 0:
		result.NewerSide = ""
		result.Reason = "本地和云端文件内容一致"
	case cloudSignals > 0 && localSignals == 0:
		result.NewerSide = "cloud"
		result.Reason = cloudEvidence.Reason
		result.ReasonPath = cloudEvidence.Path
	case localSignals > 0 && cloudSignals == 0:
		result.NewerSide = "local"
		result.Reason = localEvidence.Reason
		result.ReasonPath = localEvidence.Path
	case cloudSignals > 0 && localSignals > 0:
		result.NewerSide = "both"
		result.Reason = "本地和云端都有新增或修改迹象，需要人工选择覆盖方向"
	default:
		result.NewerSide = "unknown"
		result.Reason = "文件内容不同，但修改时间没有可靠地区分新旧"
	}
	return result
}

func describeDiff(status GameStatus) (string, string) {
	if status.LocalOnly == 0 && status.CloudOnly == 0 && status.Changed == 0 {
		return "in-sync", "本地和云端存档完全一致"
	}
	if status.LastChangeSide == "cloud" {
		return "cloud-newer", "推断云端存档较新；覆盖本地前请确认"
	}
	if status.LastChangeSide == "local" {
		return "local-newer", "推断本地存档较新；覆盖云端前请确认"
	}
	return "conflict", "本地和云端存档不一致，且不能安全判断唯一较新方"
}

func copyDirectory(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := copyFile(path, target, info.Mode()); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

func copyFile(src string, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer output.Close()

	_, err = io.Copy(output, input)
	return err
}

func writeTarGz(writer io.Writer, root string) error {
	gz := gzip.NewWriter(writer)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(tw, file)
		return err
	})
}

func extractTarGz(reader io.Reader, dst string) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if filepath.Clean(filepath.FromSlash(header.Name)) == "." {
			continue
		}
		target, err := safeArchiveTarget(dst, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tr); err != nil {
				_ = file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			_ = os.Chtimes(target, header.ModTime, header.ModTime)
		default:
			return fmt.Errorf("unsupported archive entry: %s", header.Name)
		}
	}
}

func safeArchiveTarget(root string, name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path: %s", name)
	}
	return filepath.Join(root, name), nil
}

func copyDirectoryVerified(src string, dst string) error {
	if err := copyDirectory(src, dst); err != nil {
		return err
	}
	return verifyDirectoriesEqual(src, dst)
}

func verifyDirectoriesEqual(left string, right string) error {
	leftSnapshot, err := scanDirectorySnapshot(left)
	if err != nil {
		return err
	}
	rightSnapshot, err := scanDirectorySnapshot(right)
	if err != nil {
		return err
	}

	if len(leftSnapshot.Dirs) != len(rightSnapshot.Dirs) {
		return fmt.Errorf("directory count differs: %s has %d, %s has %d", left, len(leftSnapshot.Dirs), right, len(rightSnapshot.Dirs))
	}
	for dir := range leftSnapshot.Dirs {
		if _, ok := rightSnapshot.Dirs[dir]; !ok {
			return fmt.Errorf("directory missing after copy: %s", dir)
		}
	}

	if len(leftSnapshot.Files) != len(rightSnapshot.Files) {
		return fmt.Errorf("file count differs: %s has %d, %s has %d", left, len(leftSnapshot.Files), right, len(rightSnapshot.Files))
	}
	for path, leftFile := range leftSnapshot.Files {
		rightFile, ok := rightSnapshot.Files[path]
		if !ok {
			return fmt.Errorf("file missing after copy: %s", path)
		}
		if leftFile.Size != rightFile.Size || leftFile.Hash != rightFile.Hash {
			return fmt.Errorf("file content differs after copy: %s", path)
		}
	}
	return nil
}

func scanDirectorySnapshot(root string) (directorySnapshot, error) {
	files, err := scanDirectory(root)
	if err != nil {
		return directorySnapshot{}, err
	}

	snapshot := directorySnapshot{
		Files: files,
		Dirs:  map[string]struct{}{},
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." {
			snapshot.Dirs[filepath.ToSlash(rel)] = struct{}{}
		}
		return nil
	})
	return snapshot, err
}

func launchCommand(exePath string) *exec.Cmd {
	return launchCommandWithArgs(exePath, "")
}

func launchCommandWithArgs(exePath string, args string) *exec.Cmd {
	parsedArgs := parseArgs(args)
	if runtime.GOOS == "windows" && strings.ToLower(filepath.Ext(exePath)) != ".exe" {
		startArgs := []string{"/C", "start", "", exePath}
		startArgs = append(startArgs, parsedArgs...)
		cmd := exec.Command("cmd", startArgs...)
		cmd.Dir = filepath.Dir(exePath)
		hideCommandWindow(cmd)
		return cmd
	}

	cmd := exec.Command(exePath, parsedArgs...)
	cmd.Dir = filepath.Dir(exePath)
	hideCommandWindow(cmd)
	return cmd
}

func parseArgs(input string) []string {
	var args []string
	var current strings.Builder
	inQuote := false
	for _, r := range input {
		switch r {
		case '"':
			inQuote = !inQuote
		case ' ':
			if inQuote {
				current.WriteRune(r)
			} else if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

func openPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path is empty")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/C", "start", "", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	hideCommandWindow(cmd)
	return cmd.Start()
}

func candidateRootDirs(root string) []string {
	seen := map[string]struct{}{}
	var dirs []string
	add := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		if _, ok := seen[abs]; ok {
			return
		}
		seen[abs] = struct{}{}
		dirs = append(dirs, abs)
	}

	add(root)
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	return dirs
}

func waitForPort(address string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isPortOpen(address, 300*time.Millisecond) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return isPortOpen(address, 300*time.Millisecond)
}

func samePath(left string, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(leftAbs, rightAbs)
	}
	return leftAbs == rightAbs
}

func pathsNested(parent string, child string) bool {
	parentAbs, parentErr := filepath.Abs(filepath.Clean(parent))
	childAbs, childErr := filepath.Abs(filepath.Clean(child))
	if parentErr != nil || childErr != nil {
		return false
	}
	rel, err := filepath.Rel(parentAbs, childAbs)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func safeName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\\", "-")
	value = strings.ReplaceAll(value, "/", "-")

	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), ".-_")
}

func isPortOpen(address string, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
