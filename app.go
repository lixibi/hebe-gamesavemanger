package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	maxBackupsPerGame    = 5
	defaultCloudPassword = "hebesave"
)

type App struct {
	ctx          context.Context
	rootDir      string
	configPath   string
	dataDir      string
	backupDir    string
	iconCacheDir string
	sessionDir   string
	autoSessions map[string]chan struct{}
	autoLock     sync.Mutex
}

type Config struct {
	CloudServerURL string       `json:"cloudServerURL"`
	CloudPassword  string       `json:"cloudPassword"`
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
	RootDir        string       `json:"rootDir"`
	ConfigPath     string       `json:"configPath"`
	DataDir        string       `json:"dataDir"`
	CloudServerURL string       `json:"cloudServerURL"`
	CloudPassword  string       `json:"cloudPassword"`
	CloudStatus    string       `json:"cloudStatus"`
	CloudMessage   string       `json:"cloudMessage"`
	CloudGameCount int          `json:"cloudGameCount"`
	Games          []GameStatus `json:"games"`
}

type GameStatus struct {
	Game             GameConfig `json:"game"`
	IconData         string     `json:"iconData"`
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

type CompareResult struct {
	Status    GameStatus     `json:"status"`
	LocalOnly []CompareEntry `json:"localOnly"`
	CloudOnly []CompareEntry `json:"cloudOnly"`
	Changed   []CompareEntry `json:"changed"`
	Truncated bool           `json:"truncated"`
	CheckedAt string         `json:"checkedAt"`
}

type CompareEntry struct {
	Path          string `json:"path"`
	LocalSize     int64  `json:"localSize"`
	CloudSize     int64  `json:"cloudSize"`
	LocalModified string `json:"localModified"`
	CloudModified string `json:"cloudModified"`
	NewerSide     string `json:"newerSide"`
}

type TransferProgress struct {
	GameID       string `json:"gameId"`
	GameName     string `json:"gameName"`
	Direction    string `json:"direction"`
	Phase        string `json:"phase"`
	Message      string `json:"message"`
	CurrentBytes int64  `json:"currentBytes"`
	TotalBytes   int64  `json:"totalBytes"`
	CurrentPath  string `json:"currentPath"`
	Done         bool   `json:"done"`
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
	statePath     string
	baseline      directorySnapshot
	localChanged  bool
	lastObserved  string
}

type localSessionState struct {
	GameID        string            `json:"gameId"`
	StartedAt     string            `json:"startedAt"`
	LastObserved  string            `json:"lastObserved"`
	LocalChanged  bool              `json:"localChanged"`
	CloudBackedUp bool              `json:"cloudBackedUp"`
	Files         map[string]string `json:"files"`
	Dirs          []string          `json:"dirs"`
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
		iconCacheDir: filepath.Join(root, "cache", "icons"),
		sessionDir:   filepath.Join(root, "cache", "sessions"),
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

	games := a.mergeCloudGames(cfg)
	statuses := make([]GameStatus, 0, len(games))
	for _, game := range games {
		statuses = append(statuses, a.statusForGame(game))
	}

	syncStatus, syncMessage := a.cloudServerStatus(cfg.CloudServerURL)
	return AppState{
		RootDir:        a.rootDir,
		ConfigPath:     a.configPath,
		DataDir:        a.dataDir,
		CloudServerURL: cfg.CloudServerURL,
		CloudPassword:  cfg.CloudPassword,
		CloudStatus:    syncStatus,
		CloudMessage:   syncMessage,
		CloudGameCount: a.cloudGameCount(),
		Games:          statuses,
	}, nil
}

func (a *App) SaveGame(game GameConfig) (AppState, error) {
	if err := a.ensureLayout(); err != nil {
		return AppState{}, err
	}
	if err := game.normalizeAndValidate(); err != nil {
		return AppState{}, err
	}
	if err := a.saveCloudGame(game); err != nil {
		return AppState{}, fmt.Errorf("保存云端游戏配置失败：%w", err)
	}
	baseURL := a.cloudBaseURL()
	shouldInitialUpload := baseURL != "" && baseURL != "offline" && baseURL != "local" && strings.TrimSpace(game.LocalSavePath) != ""
	if shouldInitialUpload {
		manifest, err := scanDirectory(game.LocalSavePath)
		if err != nil {
			return AppState{}, fmt.Errorf("检查本地存档失败：%w", err)
		}
		if len(manifest) == 0 {
			shouldInitialUpload = false
		}
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
	if shouldInitialUpload {
		if _, err := a.uploadLocalToCloud(game, true); err != nil {
			return AppState{}, fmt.Errorf("首次上传失败：%w", err)
		}
	}
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
	if err := a.requireCloudForSync(); err != nil {
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

func (a *App) CompareGame(id string) (CompareResult, error) {
	game, err := a.findGame(id)
	if err != nil {
		return CompareResult{}, err
	}
	if err := a.requireCloudForSync(); err != nil {
		return CompareResult{}, err
	}
	localManifest, err := scanDirectory(game.LocalSavePath)
	if err != nil {
		return CompareResult{}, fmt.Errorf("扫描本地存档失败：%w", err)
	}
	cloudManifest, err := a.cloudManifest(game)
	if err != nil {
		return CompareResult{}, fmt.Errorf("读取云端存档失败：%w", err)
	}
	return a.compareGameManifests(game, localManifest, cloudManifest), nil
}

func (a *App) LaunchGame(id string) error {
	game, err := a.findGame(id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(game.GameExePath) == "" {
		return errors.New("game executable path is empty")
	}

	if isURLTarget(game.GameExePath) {
		return openExternalTarget(game.GameExePath, game.GameArgs)
	}

	cmd := launchCommandWithArgs(game.GameExePath, game.GameArgs)
	if err := cmd.Start(); err != nil {
		return err
	}
	a.startAutoUploadSession(game, cmd)
	return nil
}

func (a *App) RefreshCloudServer() (AppState, error) {
	return a.GetAppState()
}

func (a *App) SaveCloudServerURL(serverURL string, password string) (AppState, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return AppState{}, err
	}
	serverURL = normalizeCloudServerURL(serverURL)
	password = normalizeCloudPassword(password)
	cfg.CloudServerURL = serverURL
	cfg.CloudPassword = password
	if err := a.saveConfig(cfg); err != nil {
		return AppState{}, err
	}
	return a.GetAppState()
}

func (a *App) TestCloudServerURL(serverURL string, password string) (string, error) {
	serverURL = normalizeCloudServerURL(serverURL)
	password = normalizeCloudPassword(password)
	status, message := a.cloudServerStatusWithPassword(serverURL, password)
	if status != "running" {
		return message, errors.New(message)
	}
	return message, nil
}

func (a *App) ChangeCloudPassword(newPassword string) (AppState, error) {
	newPassword = strings.TrimSpace(newPassword)
	if newPassword == "" {
		return AppState{}, errors.New("new password is required")
	}
	if err := a.requireCloudForSync(); err != nil {
		return AppState{}, err
	}
	req, err := http.NewRequest(http.MethodPut, a.cloudBaseURL()+"/api/password", strings.NewReader(fmt.Sprintf(`{"password":%q}`, newPassword)))
	if err != nil {
		return AppState{}, err
	}
	a.authorizeCloudRequest(req)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return AppState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AppState{}, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return AppState{}, err
	}
	cfg.CloudPassword = newPassword
	if err := a.saveConfig(cfg); err != nil {
		return AppState{}, err
	}
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
		if isURLTarget(game.GameExePath) {
			return errors.New("URL launch target has no local game directory")
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
	totalBytes := directoryTotalBytes(game.LocalSavePath)
	currentBytes := int64(0)
	a.emitTransferProgress(TransferProgress{
		GameID:     game.ID,
		GameName:   game.Name,
		Direction:  "backup",
		Phase:      "backup",
		Message:    "正在备份当前存档",
		TotalBytes: totalBytes,
	})
	if err := copyDirectoryVerifiedWithProgress(game.LocalSavePath, backupPath, func(delta int64, rel string) {
		currentBytes += delta
		a.emitTransferProgress(TransferProgress{
			GameID:       game.ID,
			GameName:     game.Name,
			Direction:    "backup",
			Phase:        "backup",
			Message:      "正在备份当前存档",
			CurrentBytes: currentBytes,
			TotalBytes:   totalBytes,
			CurrentPath:  rel,
		})
	}); err != nil {
		return BackupInfo{}, err
	}
	if err := a.pruneBackups(id); err != nil {
		return BackupInfo{}, err
	}
	info, err := backupInfo(backupPath)
	if err != nil {
		return BackupInfo{}, err
	}
	a.emitTransferProgress(TransferProgress{
		GameID:       game.ID,
		GameName:     game.Name,
		Direction:    "backup",
		Phase:        "done",
		Message:      "备份完成",
		CurrentBytes: totalBytes,
		TotalBytes:   totalBytes,
		Done:         true,
	})
	return info, nil
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

	totalBytes := directoryTotalBytes(backupPath)
	currentBytes := int64(0)
	a.emitTransferProgress(TransferProgress{
		GameID:     game.ID,
		GameName:   game.Name,
		Direction:  "restore",
		Phase:      "restore",
		Message:    "正在还原备份",
		TotalBytes: totalBytes,
	})
	currentBackup, err := a.replaceDirectoryWithBackupProgress(game.LocalSavePath, backupPath, id, "restore", true, func(delta int64, rel string) {
		currentBytes += delta
		a.emitTransferProgress(TransferProgress{
			GameID:       game.ID,
			GameName:     game.Name,
			Direction:    "restore",
			Phase:        "restore",
			Message:      "正在还原备份",
			CurrentBytes: currentBytes,
			TotalBytes:   totalBytes,
			CurrentPath:  rel,
		})
	})
	if err != nil {
		return SyncResult{}, err
	}
	a.emitTransferProgress(TransferProgress{
		GameID:       game.ID,
		GameName:     game.Name,
		Direction:    "restore",
		Phase:        "done",
		Message:      "还原完成",
		CurrentBytes: totalBytes,
		TotalBytes:   totalBytes,
		Done:         true,
	})
	return SyncResult{
		BackupPath: currentBackup,
		Status:     a.statusForGame(game),
	}, nil
}

func (a *App) CreateCloudBackup(id string) (BackupInfo, error) {
	game, err := a.findGame(id)
	if err != nil {
		return BackupInfo{}, err
	}
	if err := a.requireCloudForSync(); err != nil {
		return BackupInfo{}, err
	}
	if a.cloudBaseURL() == "local" {
		return BackupInfo{}, errors.New("本地兼容模式没有云端备份")
	}
	req, err := http.NewRequest(http.MethodPost, a.cloudGameURL(game, "/backups"), nil)
	if err != nil {
		return BackupInfo{}, err
	}
	a.authorizeCloudRequest(req)
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return BackupInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return BackupInfo{}, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	var payload cloudBackupResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return BackupInfo{}, err
	}
	for _, backup := range payload.Backups {
		if backup.Name == payload.Name {
			return backup, nil
		}
	}
	return BackupInfo{Name: payload.Name}, nil
}

func (a *App) ListCloudBackups(id string) ([]BackupInfo, error) {
	game, err := a.findGame(id)
	if err != nil {
		return nil, err
	}
	if err := a.requireCloudForSync(); err != nil {
		return nil, err
	}
	if a.cloudBaseURL() == "local" {
		return nil, errors.New("本地兼容模式没有云端备份")
	}
	req, err := http.NewRequest(http.MethodGet, a.cloudGameURL(game, "/backups"), nil)
	if err != nil {
		return nil, err
	}
	a.authorizeCloudRequest(req)
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	var payload cloudBackupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Backups, nil
}

func (a *App) RestoreCloudBackup(id string, backupName string) (SyncResult, error) {
	game, err := a.findGame(id)
	if err != nil {
		return SyncResult{}, err
	}
	if err := a.requireCloudForSync(); err != nil {
		return SyncResult{}, err
	}
	if a.cloudBaseURL() == "local" {
		return SyncResult{}, errors.New("本地兼容模式没有云端备份")
	}
	if backupName != filepath.Base(backupName) || strings.TrimSpace(backupName) == "" {
		return SyncResult{}, errors.New("invalid backup name")
	}
	req, err := http.NewRequest(http.MethodPost, a.cloudGameURL(game, "/backups/restore/"+url.PathEscape(backupName)), nil)
	if err != nil {
		return SyncResult{}, err
	}
	a.authorizeCloudRequest(req)
	client := http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return SyncResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return SyncResult{}, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	var payload cloudUploadResponse
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	return SyncResult{BackupPath: "cloud:" + payload.Backup, Status: a.statusForGame(game)}, nil
}

func (a *App) ensureLayout() error {
	for _, dir := range []string{
		filepath.Dir(a.configPath),
		a.dataDir,
		a.backupDir,
		a.iconCacheDir,
		a.sessionDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if _, err := os.Stat(a.configPath); errors.Is(err, os.ErrNotExist) {
		return a.saveConfig(Config{CloudPassword: defaultCloudPassword, Games: []GameConfig{}})
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
		return Config{CloudPassword: defaultCloudPassword, Games: []GameConfig{}}, nil
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.CloudPassword) == "" {
		cfg.CloudPassword = defaultCloudPassword
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
	for _, game := range a.mergeCloudGames(cfg) {
		if game.ID == id {
			return game, nil
		}
	}
	return GameConfig{}, fmt.Errorf("game not found: %s", id)
}

func (a *App) mergeCloudGames(cfg Config) []GameConfig {
	localByID := map[string]GameConfig{}
	localByFolder := map[string]GameConfig{}
	for _, game := range cfg.Games {
		game.applyDefaults()
		localByID[game.ID] = game
		localByFolder[game.FolderName] = game
	}

	cloudGames, err := a.loadCloudGames()
	if err != nil || len(cloudGames) == 0 {
		return cfg.Games
	}

	merged := make([]GameConfig, 0, len(cloudGames))
	seen := map[string]struct{}{}
	for _, cloud := range cloudGames {
		base := GameConfig{
			ID:                        normalizeGameIdentifier(firstNonEmpty(cloud.ID, cloud.FolderName)),
			Name:                      strings.TrimSpace(cloud.Name),
			FolderName:                normalizeGameIdentifier(firstNonEmpty(cloud.FolderName, cloud.ID)),
			AutoUploadMode:            "manual",
			AutoUploadIntervalMinutes: 5,
		}
		if base.Name == "" {
			base.Name = base.FolderName
		}
		if local, ok := localByID[base.ID]; ok {
			base.LocalSavePath = local.LocalSavePath
			base.GameExePath = local.GameExePath
			base.GameArgs = local.GameArgs
			base.AutoUploadMode = local.AutoUploadMode
			base.AutoUploadIntervalMinutes = local.AutoUploadIntervalMinutes
		} else if local, ok := localByFolder[base.FolderName]; ok {
			base.LocalSavePath = local.LocalSavePath
			base.GameExePath = local.GameExePath
			base.GameArgs = local.GameArgs
			base.AutoUploadMode = local.AutoUploadMode
			base.AutoUploadIntervalMinutes = local.AutoUploadIntervalMinutes
		}
		base.applyDefaults()
		merged = append(merged, base)
		seen[base.ID] = struct{}{}
	}
	for _, local := range cfg.Games {
		local.applyDefaults()
		if _, ok := seen[local.ID]; !ok {
			merged = append(merged, local)
		}
	}
	sort.Slice(merged, func(i, j int) bool {
		return strings.ToLower(merged[i].Name) < strings.ToLower(merged[j].Name)
	})
	return merged
}

func (a *App) statusForGame(game GameConfig) GameStatus {
	game.applyDefaults()
	status := GameStatus{
		Game:          game,
		IconData:      a.gameIconData(game),
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
	if (diff.LocalOnly > 0 || diff.CloudOnly > 0 || diff.Changed > 0) && a.localSessionShowsCurrentLocalChanged(game) {
		if status.LastChangeSide == "" || status.LastChangeSide == "unknown" {
			status.LastChangeSide = "local"
			status.LastChangeReason = "本地存档目录在游戏启动后发生文件变化"
			status.LastChangePath = ""
		}
	}
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
	Dirs []string `json:"dirs"`
}

type cloudUploadResponse struct {
	Backup string `json:"backup"`
}

type cloudBackupResponse struct {
	Name    string       `json:"name"`
	Backups []BackupInfo `json:"backups"`
}

type cloudBackupsResponse struct {
	Backups []BackupInfo `json:"backups"`
}

type cloudGameConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	FolderName string `json:"folderName"`
}

type cloudGamesResponse struct {
	Games []cloudGameConfig `json:"games"`
}

func (a *App) cloudBaseURL() string {
	cfg, err := a.loadConfig()
	if err != nil || strings.TrimSpace(cfg.CloudServerURL) == "" {
		return ""
	}
	return normalizeCloudServerURL(cfg.CloudServerURL)
}

func (a *App) requireCloudForSync() error {
	baseURL := a.cloudBaseURL()
	switch baseURL {
	case "":
		return errors.New("云端未设置，请先配置云服务或选择离线使用；离线时只能本地备份，不能同步")
	case "offline":
		return errors.New("当前是离线使用模式，只能本地备份，不能进行云端同步或对比")
	default:
		return nil
	}
}

func (a *App) cloudPassword() string {
	cfg, err := a.loadConfig()
	if err != nil || strings.TrimSpace(cfg.CloudPassword) == "" {
		return defaultCloudPassword
	}
	return cfg.CloudPassword
}

func (a *App) authorizeCloudRequest(req *http.Request) {
	baseURL := a.cloudBaseURL()
	if baseURL != "local" && baseURL != "offline" && baseURL != "" {
		req.Header.Set("X-Hebe-Password", a.cloudPassword())
	}
}

func (a *App) cloudGameURL(game GameConfig, suffix string) string {
	game.applyDefaults()
	return a.cloudBaseURL() + "/api/games/" + url.PathEscape(game.FolderName) + suffix
}

func (a *App) loadCloudGames() ([]cloudGameConfig, error) {
	baseURL := a.cloudBaseURL()
	if baseURL == "" || baseURL == "offline" {
		return []cloudGameConfig{}, nil
	}
	if baseURL == "local" {
		cfg, err := a.loadConfig()
		if err != nil {
			return nil, err
		}
		games := make([]cloudGameConfig, 0, len(cfg.Games))
		for _, game := range cfg.Games {
			game.applyDefaults()
			games = append(games, cloudGameConfig{ID: game.ID, Name: game.Name, FolderName: game.FolderName})
		}
		return games, nil
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/games", nil)
	if err != nil {
		return nil, err
	}
	a.authorizeCloudRequest(req)
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	var payload cloudGamesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Games, nil
}

func (a *App) cloudGameCount() int {
	games, err := a.loadCloudGames()
	if err != nil {
		return 0
	}
	return len(games)
}

func (a *App) saveCloudGame(game GameConfig) error {
	baseURL := a.cloudBaseURL()
	if baseURL == "" || baseURL == "offline" || baseURL == "local" {
		return nil
	}
	payload := cloudGameConfig{ID: game.ID, Name: game.Name, FolderName: game.FolderName}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPut, a.cloudGameURL(game, "/config"), strings.NewReader(string(raw)))
	if err != nil {
		return err
	}
	a.authorizeCloudRequest(req)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloud server returned %s", resp.Status)
	}
	return nil
}

func (a *App) cloudManifest(game GameConfig) (map[string]fileInfo, error) {
	snapshot, err := a.cloudSnapshot(game)
	if err != nil {
		return nil, err
	}
	return snapshot.Files, nil
}

func (a *App) cloudSnapshot(game GameConfig) (directorySnapshot, error) {
	baseURL := a.cloudBaseURL()
	switch baseURL {
	case "":
		return directorySnapshot{}, errors.New("云端未设置")
	case "offline":
		return directorySnapshot{}, errors.New("离线使用模式未连接云端")
	case "local":
		return scanDirectorySnapshot(a.cloudSavePath(game))
	}
	req, err := http.NewRequest(http.MethodGet, a.cloudGameURL(game, "/manifest"), nil)
	if err != nil {
		return directorySnapshot{}, err
	}
	a.authorizeCloudRequest(req)
	client := http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return directorySnapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return directorySnapshot{}, fmt.Errorf("cloud server returned %s", resp.Status)
	}
	var payload cloudManifestResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return directorySnapshot{}, err
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
	dirs := map[string]struct{}{}
	for _, dir := range payload.Dirs {
		dirs[filepath.ToSlash(dir)] = struct{}{}
	}
	return directorySnapshot{Files: files, Dirs: dirs}, nil
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
	a.authorizeCloudRequest(req)
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
		a.emitTransferProgress(TransferProgress{GameID: game.ID, GameName: game.Name, Direction: "local-to-cloud", Phase: "copy", Message: "正在复制到本地云目录"})
		defer a.emitTransferProgress(TransferProgress{GameID: game.ID, GameName: game.Name, Direction: "local-to-cloud", Phase: "done", Message: "上传完成", Done: true})
		return a.replaceDirectoryWithBackup(a.cloudSavePath(game), game.LocalSavePath, game.ID, "auto-upload", backup)
	}
	localManifest, err := scanDirectory(game.LocalSavePath)
	if err != nil {
		return "", err
	}
	_, totalBytes, _, _ := manifestStats(localManifest)
	a.emitTransferProgress(TransferProgress{GameID: game.ID, GameName: game.Name, Direction: "local-to-cloud", Phase: "pack", Message: "正在打包本地存档", TotalBytes: totalBytes})
	reader, writer := io.Pipe()
	go func() {
		var sent int64
		err := writeTarGzWithProgress(writer, game.LocalSavePath, func(delta int64, rel string) {
			sent += delta
			a.emitTransferProgress(TransferProgress{
				GameID:       game.ID,
				GameName:     game.Name,
				Direction:    "local-to-cloud",
				Phase:        "upload",
				Message:      "正在上传本地存档",
				CurrentBytes: sent,
				TotalBytes:   totalBytes,
				CurrentPath:  rel,
			})
		})
		_ = writer.CloseWithError(err)
	}()
	req, err := http.NewRequest(http.MethodPut, a.cloudGameURL(game, "/archive"), reader)
	if err != nil {
		return "", err
	}
	a.authorizeCloudRequest(req)
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
		err := a.verifyCloudMatchesLocal(game)
		a.emitTransferProgress(TransferProgress{GameID: game.ID, GameName: game.Name, Direction: "local-to-cloud", Phase: "done", Message: "上传完成", CurrentBytes: totalBytes, TotalBytes: totalBytes, Done: true})
		return "", err
	}
	err = a.verifyCloudMatchesLocal(game)
	a.emitTransferProgress(TransferProgress{GameID: game.ID, GameName: game.Name, Direction: "local-to-cloud", Phase: "done", Message: "上传完成", CurrentBytes: totalBytes, TotalBytes: totalBytes, Done: true})
	return "cloud:" + payload.Backup, err
}

func (a *App) emitTransferProgress(progress TransferProgress) {
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "transfer-progress", progress)
	}
}

func (a *App) verifyCloudMatchesLocal(game GameConfig) error {
	localSnapshot, err := scanDirectorySnapshot(game.LocalSavePath)
	if err != nil {
		return err
	}
	cloudSnapshot, err := a.cloudSnapshot(game)
	if err != nil {
		return err
	}
	return verifySnapshotsEqual(localSnapshot, cloudSnapshot, "local", "cloud")
}

func (a *App) cloudServerStatus(baseURL string) (string, string) {
	return a.cloudServerStatusWithPassword(baseURL, a.cloudPassword())
}

func (a *App) cloudServerStatusWithPassword(baseURL string, password string) (string, string) {
	if strings.TrimSpace(baseURL) == "" {
		return "unset", "云端未设置：可以本地备份，但不能上传、下载或对比云端"
	}
	baseURL = normalizeCloudServerURL(baseURL)
	password = normalizeCloudPassword(password)
	if baseURL == "offline" {
		return "offline", "离线使用：只启用本地备份，不连接云端"
	}
	if baseURL == "local" {
		return "running", "本地 data 兼容模式，仅用于测试或离线调试"
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/games", nil)
	if err != nil {
		return "stopped", err.Error()
	}
	req.Header.Set("X-Hebe-Password", password)
	client := http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return "stopped", fmt.Sprintf("云服务未连接：%s", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "stopped", "云服务密码错误"
	}
	if resp.StatusCode != http.StatusOK {
		return "stopped", fmt.Sprintf("云服务返回 %s", resp.Status)
	}
	return "running", "自建云存档服务已连接：" + strings.TrimRight(baseURL, "/")
}

func (a *App) replaceDirectory(dst string, src string, gameID string, direction string) (string, error) {
	return a.replaceDirectoryWithBackup(dst, src, gameID, direction, true)
}

func (a *App) replaceDirectoryWithBackup(dst string, src string, gameID string, direction string, keepBackup bool) (string, error) {
	return a.replaceDirectoryWithBackupProgress(dst, src, gameID, direction, keepBackup, nil)
}

func (a *App) replaceDirectoryWithBackupProgress(dst string, src string, gameID string, direction string, keepBackup bool, progress func(delta int64, rel string)) (string, error) {
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
	if err := copyDirectoryVerifiedWithProgress(src, stage, progress); err != nil {
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

func (a *App) gameIconData(game GameConfig) string {
	exePath := strings.TrimSpace(game.GameExePath)
	if runtime.GOOS != "windows" || exePath == "" || isURLTarget(exePath) || strings.ToLower(filepath.Ext(exePath)) != ".exe" {
		return ""
	}
	info, err := os.Stat(exePath)
	if err != nil || info.IsDir() {
		return ""
	}
	cachePath := filepath.Join(a.iconCacheDir, safeName(firstNonEmpty(game.ID, game.FolderName))+".png")
	if cacheInfo, err := os.Stat(cachePath); err != nil || cacheInfo.ModTime().Before(info.ModTime()) {
		if err := extractWindowsExeIcon(exePath, cachePath); err != nil {
			return ""
		}
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil || len(raw) == 0 {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
}

func extractWindowsExeIcon(exePath string, pngPath string) error {
	if err := os.MkdirAll(filepath.Dir(pngPath), 0o755); err != nil {
		return err
	}
	script := `$ErrorActionPreference = 'Stop'; Add-Type -AssemblyName System.Drawing; $icon = [System.Drawing.Icon]::ExtractAssociatedIcon($args[0]); if ($null -eq $icon) { exit 2 }; $bmp = $icon.ToBitmap(); $bmp.Save($args[1], [System.Drawing.Imaging.ImageFormat]::Png); $bmp.Dispose(); $icon.Dispose()`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script, exePath, pngPath)
	hideCommandWindow(cmd)
	return cmd.Run()
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
	session, err := a.newAutoUploadSession(game)
	if err != nil {
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
					_ = a.observeLocalSessionChange(session)
					_ = a.autoUploadIfLocalNewer(session)
				case <-processDone:
					_ = a.observeLocalSessionChange(session)
					_ = a.autoUploadIfLocalNewer(session)
					return
				}
			}
		}

		monitorTicker := time.NewTicker(30 * time.Second)
		defer monitorTicker.Stop()
		for {
			select {
			case <-done:
				return
			case <-monitorTicker.C:
				_ = a.observeLocalSessionChange(session)
			case <-processDone:
				_ = a.observeLocalSessionChange(session)
				if game.AutoUploadMode == "on-exit" {
					_ = a.autoUploadIfLocalNewer(session)
					return
				}
				if game.AutoUploadMode == "ask-on-exit" {
					a.promptUploadIfLocalNewer(session)
				}
				return
			}
		}
	}()
}

func (a *App) promptUploadIfLocalNewer(session *autoUploadSession) {
	status := a.statusForGame(session.game)
	if !session.localChanged && status.LastChangeSide != "local" && status.State != "missing-cloud" {
		return
	}
	if a.ctx != nil {
		wailsruntime.EventsEmit(a.ctx, "game-local-newer-after-exit", status)
	}
}

func (a *App) autoUploadIfLocalNewer(session *autoUploadSession) error {
	if err := a.observeLocalSessionChange(session); err != nil {
		return err
	}
	status := a.statusForGame(session.game)
	if !session.localChanged && status.LastChangeSide != "local" && status.State != "missing-cloud" {
		return nil
	}
	backup := !session.cloudBackedUp
	if _, err := a.uploadLocalToCloud(session.game, backup); err != nil {
		return err
	}
	session.cloudBackedUp = true
	session.localChanged = false
	if snapshot, err := scanDirectorySnapshot(session.game.LocalSavePath); err == nil {
		session.baseline = snapshot
	}
	_ = a.saveLocalSessionState(session)
	return nil
}

func (a *App) newAutoUploadSession(game GameConfig) (*autoUploadSession, error) {
	snapshot, err := scanDirectorySnapshot(game.LocalSavePath)
	if err != nil {
		return nil, err
	}
	session := &autoUploadSession{
		game:         game,
		statePath:    a.localSessionPath(game.ID),
		baseline:     snapshot,
		lastObserved: time.Now().Format(time.RFC3339),
	}
	if state, err := a.loadLocalSessionState(game.ID); err == nil {
		session.cloudBackedUp = state.CloudBackedUp
		if state.LocalChanged {
			session.baseline = state.toSnapshot()
			session.localChanged = true
		} else {
			session.baseline = state.toSnapshot()
		}
	}
	if err := a.saveLocalSessionState(session); err != nil {
		return nil, err
	}
	return session, nil
}

func (a *App) observeLocalSessionChange(session *autoUploadSession) error {
	current, err := scanDirectorySnapshot(session.game.LocalSavePath)
	if err != nil {
		return err
	}
	if err := verifySnapshotsEqual(session.baseline, current, "session-start", "local-now"); err != nil {
		session.localChanged = true
	}
	session.lastObserved = time.Now().Format(time.RFC3339)
	return a.saveLocalSessionState(session)
}

func (a *App) saveLocalSessionState(session *autoUploadSession) error {
	if err := os.MkdirAll(a.sessionDir, 0o755); err != nil {
		return err
	}
	if strings.TrimSpace(session.statePath) == "" {
		session.statePath = a.localSessionPath(session.game.ID)
	}
	state := localSessionState{
		GameID:        session.game.ID,
		StartedAt:     time.Now().Format(time.RFC3339),
		LastObserved:  firstNonEmpty(session.lastObserved, time.Now().Format(time.RFC3339)),
		LocalChanged:  session.localChanged,
		CloudBackedUp: session.cloudBackedUp,
		Files:         snapshotFileTokens(session.baseline),
		Dirs:          snapshotDirs(session.baseline),
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(session.statePath, append(raw, '\n'), 0o644)
}

func (a *App) loadLocalSessionState(gameID string) (localSessionState, error) {
	raw, err := os.ReadFile(a.localSessionPath(gameID))
	if err != nil {
		return localSessionState{}, err
	}
	var state localSessionState
	if err := json.Unmarshal(raw, &state); err != nil {
		return localSessionState{}, err
	}
	return state, nil
}

func (a *App) localSessionShowsCurrentLocalChanged(game GameConfig) bool {
	state, err := a.loadLocalSessionState(game.ID)
	if err != nil || !state.LocalChanged {
		return false
	}
	current, err := scanDirectorySnapshot(game.LocalSavePath)
	if err != nil {
		return false
	}
	return verifySnapshotsEqual(state.toSnapshot(), current, "session-start", "local-now") != nil
}

func (a *App) localSessionPath(gameID string) string {
	return filepath.Join(a.sessionDir, safeName(gameID)+".json")
}

func (state localSessionState) toSnapshot() directorySnapshot {
	files := map[string]fileInfo{}
	for path, token := range state.Files {
		parts := strings.Split(token, "|")
		if len(parts) != 3 {
			continue
		}
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		modTime, _ := time.Parse(time.RFC3339Nano, parts[2])
		files[path] = fileInfo{Hash: parts[0], Size: size, ModTime: modTime}
	}
	dirs := map[string]struct{}{}
	for _, dir := range state.Dirs {
		dirs[dir] = struct{}{}
	}
	return directorySnapshot{Files: files, Dirs: dirs}
}

func snapshotFileTokens(snapshot directorySnapshot) map[string]string {
	tokens := map[string]string{}
	for path, file := range snapshot.Files {
		tokens[path] = fmt.Sprintf("%s|%d|%s", file.Hash, file.Size, file.ModTime.Format(time.RFC3339Nano))
	}
	return tokens
}

func snapshotDirs(snapshot directorySnapshot) []string {
	dirs := make([]string, 0, len(snapshot.Dirs))
	for dir := range snapshot.Dirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
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

func (g *GameConfig) normalizeAndValidate() error {
	g.Name = strings.TrimSpace(g.Name)
	g.ID = normalizeGameIdentifier(g.ID)
	g.FolderName = normalizeGameIdentifier(g.FolderName)
	g.LocalSavePath = strings.TrimSpace(g.LocalSavePath)
	g.GameExePath = strings.TrimSpace(g.GameExePath)
	g.GameArgs = strings.TrimSpace(g.GameArgs)
	g.AutoUploadMode = strings.TrimSpace(g.AutoUploadMode)
	g.SaveSubdir = ""

	if g.Name == "" {
		return errors.New("game name is required")
	}
	if g.FolderName == "" {
		g.FolderName = defaultGameIdentifier(g.Name)
	}
	if err := validateGameIdentifier(g.FolderName); err != nil {
		return err
	}
	if g.ID == "" {
		g.ID = g.FolderName
	}
	g.applyDefaults()
	return nil
}

func (g *GameConfig) applyDefaults() {
	if strings.TrimSpace(g.ID) == "" {
		g.ID = normalizeGameIdentifier(g.FolderName)
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

func (a *App) compareGameManifests(game GameConfig, local map[string]fileInfo, cloud map[string]fileInfo) CompareResult {
	const maxEntriesPerGroup = 30

	localOnlyPaths := []string{}
	cloudOnlyPaths := []string{}
	changedPaths := []string{}
	for path, localFile := range local {
		cloudFile, ok := cloud[path]
		if !ok {
			localOnlyPaths = append(localOnlyPaths, path)
			continue
		}
		if localFile.Hash != cloudFile.Hash {
			changedPaths = append(changedPaths, path)
		}
	}
	for path := range cloud {
		if _, ok := local[path]; !ok {
			cloudOnlyPaths = append(cloudOnlyPaths, path)
		}
	}
	sort.Strings(localOnlyPaths)
	sort.Strings(cloudOnlyPaths)
	sort.Strings(changedPaths)

	status := a.statusForGame(game)
	result := CompareResult{
		Status:    status,
		CheckedAt: time.Now().Format(time.RFC3339),
	}
	result.LocalOnly, result.Truncated = compareEntries(localOnlyPaths, local, cloud, maxEntriesPerGroup, result.Truncated)
	result.CloudOnly, result.Truncated = compareEntries(cloudOnlyPaths, local, cloud, maxEntriesPerGroup, result.Truncated)
	result.Changed, result.Truncated = compareEntries(changedPaths, local, cloud, maxEntriesPerGroup, result.Truncated)
	return result
}

func compareEntries(paths []string, local map[string]fileInfo, cloud map[string]fileInfo, limit int, truncated bool) ([]CompareEntry, bool) {
	if len(paths) > limit {
		truncated = true
		paths = paths[:limit]
	}
	entries := make([]CompareEntry, 0, len(paths))
	for _, path := range paths {
		localFile, hasLocal := local[path]
		cloudFile, hasCloud := cloud[path]
		entry := CompareEntry{Path: path}
		if hasLocal {
			entry.LocalSize = localFile.Size
			entry.LocalModified = localFile.ModTime.Format(time.RFC3339)
		}
		if hasCloud {
			entry.CloudSize = cloudFile.Size
			entry.CloudModified = cloudFile.ModTime.Format(time.RFC3339)
		}
		entry.NewerSide = compareEntryNewerSide(localFile, hasLocal, cloudFile, hasCloud)
		entries = append(entries, entry)
	}
	return entries, truncated
}

func compareEntryNewerSide(localFile fileInfo, hasLocal bool, cloudFile fileInfo, hasCloud bool) string {
	const modTimeTolerance = 2 * time.Second
	switch {
	case hasLocal && !hasCloud:
		return "local"
	case hasCloud && !hasLocal:
		return "cloud"
	case !hasLocal && !hasCloud:
		return ""
	case localFile.ModTime.After(cloudFile.ModTime.Add(modTimeTolerance)):
		return "local"
	case cloudFile.ModTime.After(localFile.ModTime.Add(modTimeTolerance)):
		return "cloud"
	default:
		return "unknown"
	}
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
	return copyDirectoryWithProgress(src, dst, nil)
}

func copyDirectoryWithProgress(src string, dst string, progress func(delta int64, rel string)) error {
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
		if err := copyFileWithProgress(path, target, info.Mode(), filepath.ToSlash(rel), progress); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

func copyFile(src string, dst string, mode os.FileMode) error {
	return copyFileWithProgress(src, dst, mode, "", nil)
}

func copyFileWithProgress(src string, dst string, mode os.FileMode, rel string, progress func(delta int64, rel string)) error {
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

	_, err = copyWithProgress(output, input, rel, progress)
	return err
}

func writeTarGz(writer io.Writer, root string) error {
	return writeTarGzWithProgress(writer, root, nil)
}

func writeTarGzWithProgress(writer io.Writer, root string, progress func(delta int64, rel string)) error {
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
		relSlash := filepath.ToSlash(rel)
		header.Name = relSlash
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
		_, copyErr := copyWithProgress(tw, file, relSlash, progress)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func copyWithProgress(dst io.Writer, src io.Reader, rel string, progress func(delta int64, rel string)) (int64, error) {
	if progress == nil {
		return io.Copy(dst, src)
	}
	buffer := make([]byte, 256*1024)
	var written int64
	for {
		nr, readErr := src.Read(buffer)
		if nr > 0 {
			nw, writeErr := dst.Write(buffer[:nr])
			if nw > 0 {
				delta := int64(nw)
				written += delta
				progress(delta, rel)
			}
			if writeErr != nil {
				return written, writeErr
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
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
	return copyDirectoryVerifiedWithProgress(src, dst, nil)
}

func copyDirectoryVerifiedWithProgress(src string, dst string, progress func(delta int64, rel string)) error {
	if err := copyDirectoryWithProgress(src, dst, progress); err != nil {
		return err
	}
	return verifyDirectoriesEqual(src, dst)
}

func directoryTotalBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
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
	return verifySnapshotsEqual(leftSnapshot, rightSnapshot, left, right)
}

func verifySnapshotsEqual(leftSnapshot directorySnapshot, rightSnapshot directorySnapshot, leftName string, rightName string) error {
	if len(leftSnapshot.Dirs) != len(rightSnapshot.Dirs) {
		return fmt.Errorf("directory count differs: %s has %d, %s has %d", leftName, len(leftSnapshot.Dirs), rightName, len(rightSnapshot.Dirs))
	}
	for dir := range leftSnapshot.Dirs {
		if _, ok := rightSnapshot.Dirs[dir]; !ok {
			return fmt.Errorf("directory missing after copy: %s", dir)
		}
	}

	if len(leftSnapshot.Files) != len(rightSnapshot.Files) {
		return fmt.Errorf("file count differs: %s has %d, %s has %d", leftName, len(leftSnapshot.Files), rightName, len(rightSnapshot.Files))
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

func openExternalTarget(target string, args string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("target is empty")
	}
	parts := append([]string{target}, parseArgs(args)...)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		startArgs := []string{"/C", "start", ""}
		startArgs = append(startArgs, parts...)
		cmd = exec.Command("cmd", startArgs...)
	case "darwin":
		cmd = exec.Command("open", parts...)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	hideCommandWindow(cmd)
	return cmd.Start()
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
	if isURLTarget(path) {
		return openExternalTarget(path, "")
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

func isURLTarget(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" {
		return false
	}
	if len(parsed.Scheme) == 1 && runtime.GOOS == "windows" {
		return false
	}
	return true
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

func defaultGameIdentifier(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(value) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_':
			b.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r):
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-_")
}

func normalizeGameIdentifier(value string) string {
	return strings.TrimSpace(value)
}

func validateGameIdentifier(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("游戏标识名不能为空")
	}
	if value == "." || value == ".." || strings.HasPrefix(value, ".") {
		return errors.New("游戏标识名不能以点开头")
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return errors.New("游戏标识名只能包含中文、字母、数字、- 或 _")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeCloudServerURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if value == "local" || value == "offline" {
		return value
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	return strings.TrimRight(value, "/")
}

func normalizeCloudPassword(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultCloudPassword
	}
	return value
}
