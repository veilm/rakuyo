package main

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

const authCookieName = "rakuyo_auth"

type multiStringFlag []string

func (m *multiStringFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiStringFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

type rootMount struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
	Real string
}

type app struct {
	roots     []rootMount
	histDir   string
	password  string
	authToken string
	thumbMu   sync.Map
}

type listEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
	Mime    string `json:"mime,omitempty"`
	Thumb   bool   `json:"thumb"`
}

type listResponse struct {
	RootID   int         `json:"rootId"`
	RootName string      `json:"rootName"`
	RootPath string      `json:"rootPath"`
	Path     string      `json:"path"`
	Parent   string      `json:"parent,omitempty"`
	Entries  []listEntry `json:"entries"`
}

func main() {
	var dirs multiStringFlag
	var addr string
	var password string
	var hist string

	flag.Var(&dirs, "d", "host path to expose (repeatable)")
	flag.StringVar(&addr, "addr", ":8080", "listen address")
	flag.StringVar(&password, "password", "", "optional shared password")
	flag.StringVar(&hist, "hist", "", "thumbnail cache directory")
	flag.Parse()

	if len(dirs) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			log.Fatal(err)
		}
		dirs = append(dirs, cwd)
	}

	if hist == "" {
		hist = filepath.Join(os.TempDir(), "rakuyo-hist")
	}

	histPath, err := expandPath(hist)
	if err != nil {
		log.Fatalf("invalid --hist: %v", err)
	}
	if err := os.MkdirAll(histPath, 0o755); err != nil {
		log.Fatalf("failed to create --hist directory: %v", err)
	}

	roots := make([]rootMount, 0, len(dirs))
	for i, raw := range dirs {
		expanded, err := expandPath(raw)
		if err != nil {
			log.Fatalf("invalid -d %q: %v", raw, err)
		}
		abs, err := filepath.Abs(expanded)
		if err != nil {
			log.Fatalf("invalid -d %q: %v", raw, err)
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			log.Fatalf("invalid -d %q: %v", raw, err)
		}
		st, err := os.Stat(real)
		if err != nil {
			log.Fatalf("invalid -d %q: %v", raw, err)
		}
		if !st.IsDir() {
			log.Fatalf("-d %q is not a directory", raw)
		}

		name := filepath.Base(real)
		if name == string(filepath.Separator) || name == "." || name == "" {
			name = real
		}

		roots = append(roots, rootMount{ID: i, Name: name, Path: abs, Real: real})
	}

	a := &app{
		roots:    roots,
		histDir:  histPath,
		password: password,
	}
	if password != "" {
		tok := sha256.Sum256([]byte("rakuyo|" + password))
		a.authToken = hex.EncodeToString(tok[:])
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", a.handleLogin)
	mux.HandleFunc("/api/logout", a.handleLogout)
	mux.HandleFunc("/api/roots", a.withAuth(a.handleRoots))
	mux.HandleFunc("/api/list", a.withAuth(a.handleList))
	mux.HandleFunc("/api/file", a.withAuth(a.handleFile))
	mux.HandleFunc("/api/thumb", a.withAuth(a.handleThumb))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join("web", "index.html"))
	})

	log.Printf("rakuyo listening on %s", addr)
	for _, rt := range roots {
		log.Printf("root %d: %s (%s)", rt.ID, rt.Path, rt.Name)
	}
	log.Printf("thumb cache: %s", histPath)
	if password != "" {
		log.Printf("auth: enabled")
	} else {
		log.Printf("auth: disabled")
	}

	if err := http.ListenAndServe(addr, logRequests(mux)); err != nil {
		log.Fatal(err)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func (a *app) withAuth(next http.HandlerFunc) http.HandlerFunc {
	if a.password == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(authCookieName)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.authToken)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func (a *app) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	if a.password == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auth": false})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}

	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(a.password)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid password"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    a.authToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auth": true})
}

func (a *app) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *app) handleRoots(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"roots": a.roots})
}

func (a *app) handleList(w http.ResponseWriter, r *http.Request) {
	rootID, err := strconv.Atoi(r.URL.Query().Get("root"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid root"})
		return
	}
	root, ok := a.getRoot(rootID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "root not found"})
		return
	}

	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid path"})
		return
	}

	abs, real, err := resolveExisting(root, rel)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}

	st, err := os.Stat(real)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if !st.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "path is not a directory"})
		return
	}

	entries, err := os.ReadDir(real)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to read directory"})
		return
	}

	respEntries := make([]listEntry, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		logicalRel := filepath.ToSlash(filepath.Join(rel, name))
		logicalAbs := filepath.Join(abs, name)
		realEntry, info, err := statForListing(logicalAbs)
		if err != nil {
			continue
		}
		if !isWithin(root.Real, realEntry) {
			continue
		}

		mimeType := ""
		thumb := false
		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(name))
			mimeType = mime.TypeByExtension(ext)
			thumb = isImageExt(ext) || isVideoExt(ext)
		}

		respEntries = append(respEntries, listEntry{
			Name:    name,
			Path:    logicalRel,
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
			Mime:    mimeType,
			Thumb:   thumb,
		})
	}

	sort.Slice(respEntries, func(i, j int) bool {
		if respEntries[i].IsDir != respEntries[j].IsDir {
			return respEntries[i].IsDir
		}
		return strings.ToLower(respEntries[i].Name) < strings.ToLower(respEntries[j].Name)
	})

	parent := ""
	if rel != "" {
		parent = filepath.ToSlash(filepath.Dir(rel))
		if parent == "." {
			parent = ""
		}
	}

	writeJSON(w, http.StatusOK, listResponse{
		RootID:   root.ID,
		RootName: root.Name,
		RootPath: root.Path,
		Path:     filepath.ToSlash(rel),
		Parent:   parent,
		Entries:  respEntries,
	})
}

func (a *app) handleFile(w http.ResponseWriter, r *http.Request) {
	rootID, err := strconv.Atoi(r.URL.Query().Get("root"))
	if err != nil {
		http.Error(w, "invalid root", http.StatusBadRequest)
		return
	}
	root, ok := a.getRoot(rootID)
	if !ok {
		http.Error(w, "root not found", http.StatusNotFound)
		return
	}
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	_, real, err := resolveExisting(root, rel)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(real)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, filepath.Base(real), st.ModTime(), f)
}

func (a *app) handleThumb(w http.ResponseWriter, r *http.Request) {
	rootID, err := strconv.Atoi(r.URL.Query().Get("root"))
	if err != nil {
		http.Error(w, "invalid root", http.StatusBadRequest)
		return
	}
	root, ok := a.getRoot(rootID)
	if !ok {
		http.Error(w, "root not found", http.StatusNotFound)
		return
	}
	rel, err := cleanRel(r.URL.Query().Get("path"))
	if err != nil {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	_, real, err := resolveExisting(root, rel)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	size := 320
	if raw := r.URL.Query().Get("size"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 64 && n <= 1024 {
			size = n
		}
	}

	ext := strings.ToLower(filepath.Ext(real))
	mediaType := ""
	switch {
	case isImageExt(ext):
		mediaType = "image"
	case isVideoExt(ext):
		mediaType = "video"
	default:
		http.Error(w, "unsupported file", http.StatusBadRequest)
		return
	}

	h := sha1.New()
	io.WriteString(h, real)
	io.WriteString(h, "|")
	io.WriteString(h, mediaType)
	io.WriteString(h, "|")
	io.WriteString(h, strconv.Itoa(size))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.Size(), 10))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.ModTime().UnixNano(), 10))
	cacheKey := hex.EncodeToString(h.Sum(nil)) + ".jpg"
	cachePath := filepath.Join(a.histDir, cacheKey)

	if _, err := os.Stat(cachePath); err == nil {
		serveThumbFile(w, r, cachePath)
		return
	}

	lock := a.lockFor(cacheKey)
	lock.Lock()
	defer lock.Unlock()

	if _, err := os.Stat(cachePath); err == nil {
		serveThumbFile(w, r, cachePath)
		return
	}

	tmp := cachePath + ".tmp"
	switch mediaType {
	case "image":
		err = generateImageThumb(real, tmp, size)
	case "video":
		err = generateVideoThumb(real, tmp, size)
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("thumbnail error: %v", err), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		http.Error(w, "thumbnail cache write error", http.StatusInternalServerError)
		return
	}

	serveThumbFile(w, r, cachePath)
}

func (a *app) getRoot(id int) (rootMount, bool) {
	if id < 0 || id >= len(a.roots) {
		return rootMount{}, false
	}
	return a.roots[id], true
}

func (a *app) lockFor(key string) *sync.Mutex {
	v, ok := a.thumbMu.Load(key)
	if ok {
		return v.(*sync.Mutex)
	}
	m := &sync.Mutex{}
	actual, _ := a.thumbMu.LoadOrStore(key, m)
	return actual.(*sync.Mutex)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cleanRel(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" {
		return "", nil
	}
	clean := path.Clean("/" + raw)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path escapes root")
	}
	return filepath.FromSlash(clean), nil
}

func resolveExisting(root rootMount, rel string) (logicalAbs string, real string, err error) {
	logicalAbs = filepath.Join(root.Path, rel)
	real, err = filepath.EvalSymlinks(logicalAbs)
	if err != nil {
		return "", "", err
	}
	if !isWithin(root.Real, real) {
		return "", "", errors.New("path escapes root")
	}
	return logicalAbs, real, nil
}

func statForListing(abs string) (string, os.FileInfo, error) {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, err
	}
	return real, info, nil
}

func isWithin(rootReal, candidate string) bool {
	rootClean := filepath.Clean(rootReal)
	candClean := filepath.Clean(candidate)
	rel, err := filepath.Rel(rootClean, candClean)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}

func serveThumbFile(w http.ResponseWriter, r *http.Request, thumbPath string) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, thumbPath)
}

func generateImageThumb(src, dst string, maxSize int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}

	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w <= 0 || h <= 0 {
		return errors.New("invalid image dimensions")
	}

	tw, th := fitInside(w, h, maxSize)
	dstImg := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.CatmullRom.Scale(dstImg, dstImg.Bounds(), img, b, draw.Over, nil)

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	return jpeg.Encode(out, dstImg, &jpeg.Options{Quality: 82})
}

func generateVideoThumb(src, dst string, maxSize int) error {
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", maxSize, maxSize)
	cmd := exec.Command(
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-ss", "00:00:01",
		"-i", src,
		"-frames:v", "1",
		"-vf", vf,
		"-q:v", "4",
		"-y", dst,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(string(out)))
		}
		return err
	}
	return nil
}

func fitInside(w, h, maxSize int) (int, int) {
	if w <= maxSize && h <= maxSize {
		return w, h
	}
	if w >= h {
		return maxSize, int(float64(h) * float64(maxSize) / float64(w))
	}
	return int(float64(w) * float64(maxSize) / float64(h)), maxSize
}

func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return true
	default:
		return false
	}
}

func isVideoExt(ext string) bool {
	switch ext {
	case ".mp4", ".mkv", ".webm", ".mov", ".avi", ".m4v", ".ts":
		return true
	default:
		return false
	}
}

func expandPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
