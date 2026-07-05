package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddr       = ":27843"
	defaultDataRoot   = "/data"
	defaultPassword   = "hebesave"
	maxBackupsPerGame = 5
)

type server struct {
	root       string
	backupDir  string
	configPath string
}

type serverConfig struct {
	Password string            `json:"password"`
	Games    []cloudGameConfig `json:"games"`
}

type cloudGameConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	FolderName string `json:"folderName"`
}

type fileInfo struct {
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

type manifestResponse struct {
	Game           string              `json:"game"`
	Files          map[string]fileInfo `json:"files"`
	FileCount      int                 `json:"fileCount"`
	Bytes          int64               `json:"bytes"`
	LatestModified string              `json:"latestModified"`
	LatestPath     string              `json:"latestPath"`
}

type backupInfo struct {
	Name           string `json:"name"`
	CreatedAt      string `json:"createdAt"`
	Files          int    `json:"files"`
	Bytes          int64  `json:"bytes"`
	LatestModified string `json:"latestModified"`
	LatestPath     string `json:"latestPath"`
}

func main() {
	addr := env("HEBE_SAVE_ADDR", "")
	if addr == "" {
		port := env("HEBE_SAVE_PORT", "")
		if port == "" {
			addr = defaultAddr
		} else {
			addr = ":" + strings.TrimPrefix(port, ":")
		}
	}
	root := env("HEBE_SAVE_ROOT", defaultDataRoot)
	flag.StringVar(&addr, "addr", addr, "listen address")
	flag.StringVar(&root, "root", root, "cloud save data root")
	flag.Parse()

	s := &server{
		root:       filepath.Clean(root),
		backupDir:  filepath.Join(filepath.Clean(root), ".backups"),
		configPath: filepath.Join(filepath.Clean(root), ".hebe-games.json"),
	}
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(s.backupDir, 0o755); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("PUT /api/password", s.handleChangePassword)
	mux.HandleFunc("GET /api/games", s.handleGames)
	mux.HandleFunc("/api/games/", s.handleGame)

	log.Printf("hebe save server listening on %s, root=%s", addr, s.root)
	log.Fatal(http.ListenAndServe(addr, logRequest(mux)))
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"ok":      true,
		"root":    s.root,
		"version": "dev",
	})
}

func (s *server) handleGames(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	seen := map[string]struct{}{}
	for _, game := range cfg.Games {
		seen[game.FolderName] = struct{}{}
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != ".backups" && strings.TrimSpace(entry.Name()) != "" {
			if _, ok := seen[entry.Name()]; !ok {
				cfg.Games = append(cfg.Games, cloudGameConfig{
					ID:         entry.Name(),
					Name:       entry.Name(),
					FolderName: entry.Name(),
				})
				seen[entry.Name()] = struct{}{}
			}
		}
	}
	sort.Slice(cfg.Games, func(i, j int) bool {
		return strings.ToLower(cfg.Games[i].Name) < strings.ToLower(cfg.Games[j].Name)
	})
	writeJSON(w, map[string]any{"games": cfg.Games})
}

func (s *server) handleGame(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/games/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		writeError(w, errors.New("missing game action"), http.StatusNotFound)
		return
	}
	game, err := cleanName(parts[0])
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}

	switch {
	case len(parts) == 2 && parts[1] == "config" && r.Method == http.MethodPut:
		s.handleSaveGameConfig(w, r, game)
	case len(parts) == 2 && parts[1] == "manifest" && r.Method == http.MethodGet:
		s.handleManifest(w, game)
	case len(parts) == 2 && parts[1] == "archive" && r.Method == http.MethodGet:
		s.handleDownloadArchive(w, game)
	case len(parts) == 2 && parts[1] == "archive" && (r.Method == http.MethodPost || r.Method == http.MethodPut):
		s.handleUploadArchive(w, r, game)
	case len(parts) == 2 && parts[1] == "backups" && r.Method == http.MethodGet:
		s.handleListBackups(w, game)
	case len(parts) == 2 && parts[1] == "backups" && r.Method == http.MethodPost:
		s.handleCreateBackup(w, game)
	case len(parts) == 4 && parts[1] == "backups" && parts[3] == "archive" && r.Method == http.MethodGet:
		backup, err := cleanName(parts[2])
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		s.handleDownloadBackup(w, game, backup)
	case len(parts) == 4 && parts[1] == "backups" && parts[2] == "restore" && r.Method == http.MethodPost:
		backup, err := cleanName(parts[3])
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		s.handleRestoreBackup(w, game, backup)
	default:
		writeError(w, errors.New("unknown endpoint"), http.StatusNotFound)
	}
}

func (s *server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	var payload struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	payload.Password = strings.TrimSpace(payload.Password)
	if payload.Password == "" {
		writeError(w, errors.New("password is required"), http.StatusBadRequest)
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	cfg.Password = payload.Password
	if err := s.saveConfig(cfg); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *server) handleSaveGameConfig(w http.ResponseWriter, r *http.Request, game string) {
	var payload cloudGameConfig
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	payload.ID = safeName(firstNonEmpty(payload.ID, payload.FolderName, game))
	payload.FolderName = safeName(firstNonEmpty(payload.FolderName, game, payload.ID))
	payload.Name = strings.TrimSpace(payload.Name)
	if payload.Name == "" {
		payload.Name = payload.FolderName
	}
	if payload.FolderName != game {
		writeError(w, errors.New("game folder does not match URL"), http.StatusBadRequest)
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	replaced := false
	for i := range cfg.Games {
		if cfg.Games[i].FolderName == payload.FolderName || cfg.Games[i].ID == payload.ID {
			cfg.Games[i] = payload
			replaced = true
			break
		}
	}
	if !replaced {
		cfg.Games = append(cfg.Games, payload)
	}
	if err := s.saveConfig(cfg); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	_ = os.MkdirAll(s.gameDir(game), 0o755)
	writeJSON(w, payload)
}

func (s *server) handleManifest(w http.ResponseWriter, game string) {
	manifest, err := scanDirectory(s.gameDir(game))
	if err != nil {
		writeError(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, manifest)
}

func (s *server) handleDownloadArchive(w http.ResponseWriter, game string) {
	dir := s.gameDir(game)
	if info, err := os.Stat(dir); err != nil {
		writeError(w, err, http.StatusNotFound)
		return
	} else if !info.IsDir() {
		writeError(w, fmt.Errorf("not a directory: %s", game), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", game+".tar.gz"))
	if err := writeTarGz(w, dir); err != nil {
		log.Printf("archive %s: %v", game, err)
	}
}

func (s *server) handleUploadArchive(w http.ResponseWriter, r *http.Request, game string) {
	stage := filepath.Join(s.root, "."+game+".upload-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if err := readTarGz(r.Body, stage); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}

	backupName, err := s.replaceGameDir(game, stage, "upload")
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	manifest, err := scanDirectory(s.gameDir(game))
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"backup": backupName, "manifest": manifest})
}

func (s *server) handleListBackups(w http.ResponseWriter, game string) {
	backups, err := s.listBackups(game)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"backups": backups})
}

func (s *server) handleCreateBackup(w http.ResponseWriter, game string) {
	name, err := s.backupGame(game, "manual")
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	backups, _ := s.listBackups(game)
	writeJSON(w, map[string]any{"name": name, "backups": backups})
}

func (s *server) handleDownloadBackup(w http.ResponseWriter, game string, backup string) {
	dir := filepath.Join(s.gameBackupDir(game), backup)
	if info, err := os.Stat(dir); err != nil {
		writeError(w, err, http.StatusNotFound)
		return
	} else if !info.IsDir() {
		writeError(w, fmt.Errorf("not a backup directory: %s", backup), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", game+"-"+backup+".tar.gz"))
	if err := writeTarGz(w, dir); err != nil {
		log.Printf("backup archive %s/%s: %v", game, backup, err)
	}
}

func (s *server) handleRestoreBackup(w http.ResponseWriter, game string, backup string) {
	src := filepath.Join(s.gameBackupDir(game), backup)
	if info, err := os.Stat(src); err != nil {
		writeError(w, err, http.StatusNotFound)
		return
	} else if !info.IsDir() {
		writeError(w, fmt.Errorf("not a backup directory: %s", backup), http.StatusBadRequest)
		return
	}
	current, err := s.replaceGameDir(game, src, "restore")
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	manifest, _ := scanDirectory(s.gameDir(game))
	writeJSON(w, map[string]any{"backup": current, "manifest": manifest})
}

func (s *server) replaceGameDir(game string, src string, reason string) (string, error) {
	dst := s.gameDir(game)
	backup := ""
	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		var backupErr error
		backup, backupErr = s.backupGame(game, reason)
		if backupErr != nil {
			return "", backupErr
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	stage := filepath.Join(s.root, "."+game+".replace-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	_ = os.RemoveAll(stage)
	defer os.RemoveAll(stage)
	if err := copyDirectory(src, stage); err != nil {
		return "", err
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dst); err != nil {
		return "", err
	}
	return backup, s.pruneBackups(game)
}

func (s *server) backupGame(game string, reason string) (string, error) {
	src := s.gameDir(game)
	if info, err := os.Stat(src); err != nil {
		return "", err
	} else if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", game)
	}
	name := backupName(reason)
	dst := filepath.Join(s.gameBackupDir(game), name)
	if err := copyDirectory(src, dst); err != nil {
		return "", err
	}
	if err := s.pruneBackups(game); err != nil {
		return "", err
	}
	return name, nil
}

func (s *server) listBackups(game string) ([]backupInfo, error) {
	entries, err := os.ReadDir(s.gameBackupDir(game))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []backupInfo{}, nil
		}
		return nil, err
	}
	backups := []backupInfo{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(s.gameBackupDir(game), entry.Name())
		manifest, err := scanDirectory(dir)
		if err != nil {
			return nil, err
		}
		stat, err := entry.Info()
		if err != nil {
			return nil, err
		}
		backups = append(backups, backupInfo{
			Name:           entry.Name(),
			CreatedAt:      stat.ModTime().Format(time.RFC3339),
			Files:          manifest.FileCount,
			Bytes:          manifest.Bytes,
			LatestModified: manifest.LatestModified,
			LatestPath:     manifest.LatestPath,
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt > backups[j].CreatedAt
	})
	return backups, nil
}

func (s *server) pruneBackups(game string) error {
	backups, err := s.listBackups(game)
	if err != nil {
		return err
	}
	if len(backups) <= maxBackupsPerGame {
		return nil
	}
	for _, backup := range backups[maxBackupsPerGame:] {
		if err := os.RemoveAll(filepath.Join(s.gameBackupDir(game), backup.Name)); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) gameDir(game string) string {
	return filepath.Join(s.root, game)
}

func (s *server) gameBackupDir(game string) string {
	return filepath.Join(s.backupDir, game)
}

func (s *server) authorize(w http.ResponseWriter, r *http.Request) bool {
	cfg, err := s.loadConfig()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return false
	}
	if r.Header.Get("X-Hebe-Password") != cfg.Password {
		writeError(w, errors.New("invalid password"), http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *server) loadConfig() (serverConfig, error) {
	raw, err := os.ReadFile(s.configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return serverConfig{Password: defaultPassword, Games: []cloudGameConfig{}}, nil
		}
		return serverConfig{}, err
	}
	if strings.TrimSpace(string(raw)) == "" {
		return serverConfig{Password: defaultPassword, Games: []cloudGameConfig{}}, nil
	}
	var cfg serverConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return serverConfig{}, err
	}
	if strings.TrimSpace(cfg.Password) == "" {
		cfg.Password = defaultPassword
	}
	for i := range cfg.Games {
		cfg.Games[i].ID = safeName(firstNonEmpty(cfg.Games[i].ID, cfg.Games[i].FolderName))
		cfg.Games[i].FolderName = safeName(firstNonEmpty(cfg.Games[i].FolderName, cfg.Games[i].ID))
		cfg.Games[i].Name = strings.TrimSpace(cfg.Games[i].Name)
		if cfg.Games[i].Name == "" {
			cfg.Games[i].Name = cfg.Games[i].FolderName
		}
	}
	return cfg, nil
}

func (s *server) saveConfig(cfg serverConfig) error {
	sort.Slice(cfg.Games, func(i, j int) bool {
		return strings.ToLower(cfg.Games[i].Name) < strings.ToLower(cfg.Games[j].Name)
	})
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.configPath, append(raw, '\n'), 0o644)
}

func scanDirectory(root string) (manifestResponse, error) {
	info, err := os.Stat(root)
	if err != nil {
		return manifestResponse{}, err
	}
	if !info.IsDir() {
		return manifestResponse{}, fmt.Errorf("path is not a directory: %s", root)
	}

	files := map[string]fileInfo{}
	var bytes int64
	var latest time.Time
	latestPath := ""
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
		rel = filepath.ToSlash(rel)
		mod := info.ModTime()
		files[rel] = fileInfo{
			Hash:    hash,
			Size:    info.Size(),
			ModTime: mod.Format(time.RFC3339Nano),
		}
		bytes += info.Size()
		if mod.After(latest) || (mod.Equal(latest) && (latestPath == "" || rel < latestPath)) {
			latest = mod
			latestPath = rel
		}
		return nil
	})
	if err != nil {
		return manifestResponse{}, err
	}

	latestModified := ""
	if !latest.IsZero() {
		latestModified = latest.Format(time.RFC3339Nano)
	}
	return manifestResponse{
		Game:           filepath.Base(root),
		Files:          files,
		FileCount:      len(files),
		Bytes:          bytes,
		LatestModified: latestModified,
		LatestPath:     latestPath,
	}, nil
}

func writeTarGz(w io.Writer, root string) error {
	gz := gzip.NewWriter(w)
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
		rel = filepath.ToSlash(rel)
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel
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

func readTarGz(r io.Reader, dst string) error {
	gz, err := gzip.NewReader(r)
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
		target, err := safeJoin(dst, header.Name)
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
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		if _, err := io.Copy(output, input); err != nil {
			_ = output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
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

func safeJoin(root string, name string) (string, error) {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || strings.HasPrefix(name, ".."+string(os.PathSeparator)) || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path: %s", name)
	}
	return filepath.Join(root, name), nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid name: %s", name)
	}
	return name, nil
}

func safeName(input string) string {
	input = strings.ToLower(strings.TrimSpace(input))
	var builder strings.Builder
	lastDash := false
	for _, r := range input {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func backupName(reason string) string {
	now := time.Now()
	return fmt.Sprintf("%s_%09d_%s", now.Format("20060102_150405"), now.Nanosecond(), reason)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func env(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}
