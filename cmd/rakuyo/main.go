package main

import (
	"context"
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/draw"
)

const authCookieName = "rakuyo_auth"

var (
	unixFilenamePattern = regexp.MustCompile(`^\d{10}(?:\D|$)`)
	numericNotePattern  = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d+)?|\.\d+)$`)
)

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
	roots                 []rootMount
	histDir               string
	password              string
	authToken             string
	thumbMu               sync.Map
	thumbSem              chan struct{}
	remuxSem              chan struct{}
	interactiveUntilNanos atomic.Int64
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
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

type ffprobeOutput struct {
	Streams []ffprobeStream `json:"streams"`
	Format  ffprobeFormat   `json:"format"`
}

type ffprobeStream struct {
	Index       int               `json:"index"`
	CodecName   string            `json:"codec_name"`
	Profile     string            `json:"profile"`
	PixFmt      string            `json:"pix_fmt"`
	CodecType   string            `json:"codec_type"`
	Disposition ffDisposition     `json:"disposition"`
	Tags        map[string]string `json:"tags"`
}

type ffprobeFormat struct {
	FormatName     string `json:"format_name"`
	FormatLongName string `json:"format_long_name"`
}

type ffDisposition struct {
	Default int `json:"default"`
	Forced  int `json:"forced"`
}

type mkvTrack struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Profile  string `json:"profile,omitempty"`
	PixFmt   string `json:"pixFmt,omitempty"`
	Language string `json:"language,omitempty"`
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default"`
	Forced   bool   `json:"forced,omitempty"`
}

type mkvProbeResponse struct {
	Video  mkvTrack   `json:"video"`
	Audio  []mkvTrack `json:"audio"`
	Subs   []mkvTrack `json:"subs"`
	Source string     `json:"source"`
}

type videoProbeResponse struct {
	Video            mkvTrack   `json:"video"`
	Audio            []mkvTrack `json:"audio"`
	Source           string     `json:"source"`
	FormatName       string     `json:"formatName,omitempty"`
	FormatLongName   string     `json:"formatLongName,omitempty"`
	NativeLikely     bool       `json:"nativeLikely"`
	RemuxSupported   bool       `json:"remuxSupported"`
	RemuxRecommended bool       `json:"remuxRecommended"`
	RemuxReason      string     `json:"remuxReason,omitempty"`
}

func main() {
	var dirs multiStringFlag
	var addr string
	var password string
	var hist string

	flag.Var(&dirs, "d", "host path to expose (repeatable)")
	flag.StringVar(&addr, "addr", ":7111", "listen address")
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
		xdgDataHome := os.Getenv("XDG_DATA_HOME")
		if xdgDataHome == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				log.Fatalf("failed to determine home directory for default --hist: %v", err)
			}
			xdgDataHome = filepath.Join(home, ".local", "share")
		}
		hist = filepath.Join(xdgDataHome, "rakuyo", "hist")
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
		thumbSem: make(chan struct{}, 2),
		remuxSem: make(chan struct{}, 1),
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
	mux.HandleFunc("/api/media-note", a.withAuth(a.handleMediaNote))
	mux.HandleFunc("/api/thumb", a.withAuth(a.handleThumb))
	mux.HandleFunc("/api/video/probe", a.withAuth(a.handleVideoProbe))
	mux.HandleFunc("/api/video/play", a.withAuth(a.handleVideoPlay))
	mux.HandleFunc("/api/mkv/probe", a.withAuth(a.handleMKVProbe))
	mux.HandleFunc("/api/mkv/play", a.withAuth(a.handleMKVPlay))
	mux.HandleFunc("/api/mkv/sub", a.withAuth(a.handleMKVSub))
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join("web", "index.html"))
	})

	log.Printf("rakuyo listening on %s", addr)
	for _, rt := range roots {
		log.Printf("root %d: %s (%s)", rt.ID, rt.Path, rt.Name)
	}
	log.Printf("thumb cache: %s", histPath)
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		log.Printf("warning: ffmpeg not found, video thumbnails and remux playback will fail: %v", err)
	}
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
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		log.Printf("%s %s %d %dB %s", r.Method, r.URL.Path, rec.status, rec.bytes, time.Since(start).Round(time.Millisecond))
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
	a.markInteractiveWindow(2 * time.Second)
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

func (a *app) handleMediaNote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}

	var req struct {
		Root int    `json:"root"`
		Path string `json:"path"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
		return
	}

	root, rel, logicalAbs, real, err := a.resolvePath(req.Root, req.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	ext := strings.ToLower(filepath.Ext(real))
	if !isImageExt(ext) && !isVideoExt(ext) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not an image or video"})
		return
	}

	output, err := deriveMediaNoteOutput(req.Note)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	finalLogicalAbs := logicalAbs
	finalRel := filepath.ToSlash(rel)
	renamed := false
	if !unixFilenamePattern.MatchString(filepath.Base(finalLogicalAbs)) {
		renamedPath, err := renameWithUnixPrefix(finalLogicalAbs)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to rename file"})
			return
		}
		finalLogicalAbs = renamedPath
		finalRel = relWithRenamedBase(rel, filepath.Base(finalLogicalAbs))
		renamed = true
	}

	tags := append(tagsForPath(finalLogicalAbs), output)
	if err := runTag2CreateLink(r.Context(), finalLogicalAbs, tags); err != nil {
		if renamed {
			if revertErr := os.Rename(finalLogicalAbs, logicalAbs); revertErr != nil {
				log.Printf("media note revert failed root=%d rel=%q current=%q err=%v", root.ID, rel, finalLogicalAbs, revertErr)
				err = fmt.Errorf("%w (rename rollback failed: %v)", err, revertErr)
			}
		}
		log.Printf("media note tag2 failed root=%d rel=%q file=%q err=%v", root.ID, rel, finalLogicalAbs, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"name":    filepath.Base(finalLogicalAbs),
		"path":    finalRel,
		"output":  output,
		"renamed": renamed,
	})
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

	if err := a.acquireThumbSlot(r.Context()); err != nil {
		http.Error(w, "thumbnail canceled", http.StatusRequestTimeout)
		return
	}
	defer a.releaseThumbSlot()

	tmp := cachePath + ".tmp.jpg"
	switch mediaType {
	case "image":
		err = generateImageThumb(r.Context(), real, tmp, size)
	case "video":
		err = generateVideoThumb(r.Context(), real, tmp, size)
	}
	if err != nil {
		log.Printf("thumb failed media=%s root=%d rel=%q real=%q err=%v", mediaType, root.ID, rel, real, err)
		http.Error(w, fmt.Sprintf("thumbnail error: %v", err), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		log.Printf("thumb cache write failed root=%d rel=%q cache=%q err=%v", root.ID, rel, cachePath, err)
		http.Error(w, "thumbnail cache write error", http.StatusInternalServerError)
		return
	}

	serveThumbFile(w, r, cachePath)
}

func (a *app) handleMKVProbe(w http.ResponseWriter, r *http.Request) {
	root, rel, _, real, err := a.resolveRequestPath(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	_ = root

	if strings.ToLower(filepath.Ext(real)) != ".mkv" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not an mkv file"})
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	probe, err := probeMKV(real)
	if err != nil {
		log.Printf("mkv probe failed rel=%q real=%q err=%v", rel, real, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "probe failed"})
		return
	}
	writeJSON(w, http.StatusOK, probe)
}

func (a *app) handleVideoProbe(w http.ResponseWriter, r *http.Request) {
	_, rel, _, real, err := a.resolveRequestPath(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	ext := strings.ToLower(filepath.Ext(real))
	if !isVideoExt(ext) || ext == ".mkv" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "not a supported video file"})
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	probe, err := probeVideo(real)
	if err != nil {
		log.Printf("video probe failed rel=%q real=%q err=%v", rel, real, err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "probe failed"})
		return
	}
	writeJSON(w, http.StatusOK, probe)
}

func (a *app) handleVideoPlay(w http.ResponseWriter, r *http.Request) {
	a.markInteractiveWindow(2 * time.Second)
	_, rel, _, real, err := a.resolveRequestPath(r)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	ext := strings.ToLower(filepath.Ext(real))
	if !isVideoExt(ext) || ext == ".mkv" {
		http.Error(w, "not a supported video file", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	probe, err := probeVideo(real)
	if err != nil {
		log.Printf("video probe failed rel=%q real=%q err=%v", rel, real, err)
		http.Error(w, "probe failed", http.StatusInternalServerError)
		return
	}
	if !probe.RemuxSupported {
		http.Error(w, "video codec not supported for remux playback", http.StatusBadRequest)
		return
	}

	audioIndex, err := selectOptionalAudioIndex(r, probe.Audio)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	copyAudio := false
	audioCodec := ""
	if audioIndex >= 0 {
		audioCodec = codecForIndex(probe.Audio, audioIndex)
		copyAudio = isMP4CopyAudioCodec(audioCodec)
	}

	h := sha1.New()
	io.WriteString(h, real)
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.ModTime().UnixNano(), 10))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.Size(), 10))
	io.WriteString(h, "|")
	io.WriteString(h, probe.FormatName)
	io.WriteString(h, "|")
	io.WriteString(h, strconv.Itoa(probe.Video.Index))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.Itoa(audioIndex))
	io.WriteString(h, "|")
	if copyAudio {
		io.WriteString(h, "copy")
	} else {
		io.WriteString(h, "aac")
	}
	cachePath := filepath.Join(a.histDir, "video", hex.EncodeToString(h.Sum(nil))+".mp4")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		http.Error(w, "cache error", http.StatusInternalServerError)
		return
	}

	if _, err := os.Stat(cachePath); err != nil {
		key := "video-play:" + cachePath
		lock := a.lockFor(key)
		lock.Lock()
		if _, statErr := os.Stat(cachePath); statErr != nil {
			if err := a.acquireRemuxSlot(r.Context()); err != nil {
				lock.Unlock()
				http.Error(w, "playback canceled", http.StatusRequestTimeout)
				return
			}
			err := generateVideoPlayAsset(r.Context(), real, cachePath, probe.FormatName, probe.Video.Index, audioIndex, audioCodec, copyAudio)
			a.releaseRemuxSlot()
			if err != nil {
				lock.Unlock()
				log.Printf("video play generate failed rel=%q real=%q err=%v", rel, real, err)
				http.Error(w, "playback generation failed", http.StatusInternalServerError)
				return
			}
		}
		lock.Unlock()
	}

	out, err := os.Open(cachePath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer out.Close()
	outInfo, err := out.Stat()
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, filepath.Base(cachePath), outInfo.ModTime(), out)
}

func (a *app) handleMKVPlay(w http.ResponseWriter, r *http.Request) {
	a.markInteractiveWindow(2 * time.Second)
	root, rel, _, real, err := a.resolveRequestPath(r)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.ToLower(filepath.Ext(real)) != ".mkv" {
		http.Error(w, "not an mkv file", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	probe, err := probeMKV(real)
	if err != nil {
		log.Printf("mkv probe failed rel=%q real=%q err=%v", rel, real, err)
		http.Error(w, "probe failed", http.StatusInternalServerError)
		return
	}
	audioIndex, err := selectAudioIndex(r, probe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	videoIndex := probe.Video.Index
	audioCodec := codecForIndex(probe.Audio, audioIndex)
	copyAudio := isMP4CopyAudioCodec(audioCodec)
	copyVideo := isMP4CopyVideoTrack(probe.Video)
	if !copyVideo {
		http.Error(w, "video codec not supported for remux playback", http.StatusBadRequest)
		return
	}

	h := sha1.New()
	io.WriteString(h, real)
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.ModTime().UnixNano(), 10))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.Size(), 10))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.Itoa(videoIndex))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.Itoa(audioIndex))
	io.WriteString(h, "|")
	if copyAudio {
		io.WriteString(h, "copy")
	} else {
		io.WriteString(h, "aac")
	}
	io.WriteString(h, "|vcopy")
	cachePath := filepath.Join(a.histDir, "mkv", hex.EncodeToString(h.Sum(nil))+".mp4")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		http.Error(w, "cache error", http.StatusInternalServerError)
		return
	}

	if _, err := os.Stat(cachePath); err != nil {
		key := "mkv-play:" + cachePath
		lock := a.lockFor(key)
		lock.Lock()
		if _, statErr := os.Stat(cachePath); statErr != nil {
			if err := a.acquireRemuxSlot(r.Context()); err != nil {
				lock.Unlock()
				http.Error(w, "playback canceled", http.StatusRequestTimeout)
				return
			}
			err := a.generateMKVPlayAsset(r.Context(), real, cachePath, videoIndex, audioIndex, copyVideo, copyAudio)
			a.releaseRemuxSlot()
			if err != nil {
				lock.Unlock()
				log.Printf("mkv play generate failed rel=%q real=%q err=%v", rel, real, err)
				http.Error(w, "playback generation failed", http.StatusInternalServerError)
				return
			}
		}
		lock.Unlock()
	}

	out, err := os.Open(cachePath)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer out.Close()
	outInfo, err := out.Stat()
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, filepath.Base(cachePath), outInfo.ModTime(), out)
	_ = root
}

func (a *app) handleMKVSub(w http.ResponseWriter, r *http.Request) {
	a.markInteractiveWindow(2 * time.Second)
	_, rel, _, real, err := a.resolveRequestPath(r)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.ToLower(filepath.Ext(real)) != ".mkv" {
		http.Error(w, "not an mkv file", http.StatusBadRequest)
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	probe, err := probeMKV(real)
	if err != nil {
		log.Printf("mkv probe failed rel=%q real=%q err=%v", rel, real, err)
		http.Error(w, "probe failed", http.StatusInternalServerError)
		return
	}
	subIndex, err := selectSubIndex(r, probe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	subCodec := codecForIndex(probe.Subs, subIndex)
	if !isWebVTTConvertibleSubtitleCodec(subCodec) {
		http.Error(w, "subtitle codec not supported for webvtt", http.StatusBadRequest)
		return
	}

	h := sha1.New()
	io.WriteString(h, real)
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.ModTime().UnixNano(), 10))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.FormatInt(st.Size(), 10))
	io.WriteString(h, "|")
	io.WriteString(h, strconv.Itoa(subIndex))
	cachePath := filepath.Join(a.histDir, "mkv", hex.EncodeToString(h.Sum(nil))+".vtt")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		http.Error(w, "cache error", http.StatusInternalServerError)
		return
	}

	if _, err := os.Stat(cachePath); err != nil {
		key := "mkv-sub:" + cachePath
		lock := a.lockFor(key)
		lock.Lock()
		if _, statErr := os.Stat(cachePath); statErr != nil {
			if err := a.acquireRemuxSlot(r.Context()); err != nil {
				lock.Unlock()
				http.Error(w, "subtitle canceled", http.StatusRequestTimeout)
				return
			}
			err := generateMKVSubtitleAsset(r.Context(), real, cachePath, subIndex)
			a.releaseRemuxSlot()
			if err != nil {
				lock.Unlock()
				log.Printf("mkv subtitle generate failed rel=%q real=%q err=%v", rel, real, err)
				http.Error(w, "subtitle generation failed", http.StatusInternalServerError)
				return
			}
		}
		lock.Unlock()
	}

	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, cachePath)
}

func (a *app) resolveRequestPath(r *http.Request) (rootMount, string, string, string, error) {
	rootID, err := strconv.Atoi(r.URL.Query().Get("root"))
	if err != nil {
		return rootMount{}, "", "", "", errors.New("invalid root")
	}
	return a.resolvePath(rootID, r.URL.Query().Get("path"))
}

func (a *app) resolvePath(rootID int, rawRel string) (rootMount, string, string, string, error) {
	root, ok := a.getRoot(rootID)
	if !ok {
		return rootMount{}, "", "", "", errors.New("root not found")
	}
	rel, err := cleanRel(rawRel)
	if err != nil {
		return rootMount{}, "", "", "", errors.New("invalid path")
	}
	logicalAbs, real, err := resolveExisting(root, rel)
	if err != nil {
		return rootMount{}, "", "", "", errors.New("not found")
	}
	return root, rel, logicalAbs, real, nil
}

func (a *app) acquireThumbSlot(ctx context.Context) error {
	for {
		if time.Now().UnixNano() < a.interactiveUntilNanos.Load() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case a.thumbSem <- struct{}{}:
			return nil
		}
	}
}

func (a *app) releaseThumbSlot() {
	select {
	case <-a.thumbSem:
	default:
	}
}

func (a *app) acquireRemuxSlot(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a.remuxSem <- struct{}{}:
		return nil
	}
}

func (a *app) releaseRemuxSlot() {
	select {
	case <-a.remuxSem:
	default:
	}
}

func (a *app) markInteractiveWindow(d time.Duration) {
	now := time.Now().Add(d).UnixNano()
	for {
		prev := a.interactiveUntilNanos.Load()
		if prev >= now {
			return
		}
		if a.interactiveUntilNanos.CompareAndSwap(prev, now) {
			return
		}
	}
}

func probeMKV(src string) (mkvProbeResponse, error) {
	probe, err := ffprobeMedia(src)
	if err != nil {
		return mkvProbeResponse{}, err
	}

	resp := mkvProbeResponse{
		Audio:  make([]mkvTrack, 0, 4),
		Subs:   make([]mkvTrack, 0, 4),
		Source: filepath.Base(src),
	}
	for _, s := range probe.Streams {
		track := mkvTrack{
			Index:    s.Index,
			Codec:    s.CodecName,
			Profile:  s.Profile,
			PixFmt:   s.PixFmt,
			Language: streamTag(s.Tags, "language"),
			Title:    streamTag(s.Tags, "title"),
			Default:  s.Disposition.Default == 1,
			Forced:   s.Disposition.Forced == 1,
		}
		switch s.CodecType {
		case "video":
			if resp.Video.Index == 0 && resp.Video.Codec == "" {
				resp.Video = track
			}
		case "audio":
			resp.Audio = append(resp.Audio, track)
		case "subtitle":
			resp.Subs = append(resp.Subs, track)
		}
	}

	if resp.Video.Codec == "" {
		return mkvProbeResponse{}, errors.New("no video stream")
	}
	if len(resp.Audio) == 0 {
		return mkvProbeResponse{}, errors.New("no audio stream")
	}
	return resp, nil
}

func ffprobeMedia(src string) (ffprobeOutput, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", src)
	out, err := cmd.Output()
	if err != nil {
		return ffprobeOutput{}, err
	}
	var probe ffprobeOutput
	if err := json.Unmarshal(out, &probe); err != nil {
		return ffprobeOutput{}, err
	}
	return probe, nil
}

func probeVideo(src string) (videoProbeResponse, error) {
	probe, err := ffprobeMedia(src)
	if err != nil {
		return videoProbeResponse{}, err
	}

	resp := videoProbeResponse{
		Audio:          make([]mkvTrack, 0, 4),
		Source:         filepath.Base(src),
		FormatName:     probe.Format.FormatName,
		FormatLongName: probe.Format.FormatLongName,
	}
	for _, s := range probe.Streams {
		track := mkvTrack{
			Index:    s.Index,
			Codec:    s.CodecName,
			Profile:  s.Profile,
			PixFmt:   s.PixFmt,
			Language: streamTag(s.Tags, "language"),
			Title:    streamTag(s.Tags, "title"),
			Default:  s.Disposition.Default == 1,
			Forced:   s.Disposition.Forced == 1,
		}
		switch s.CodecType {
		case "video":
			if resp.Video.Codec == "" {
				resp.Video = track
			}
		case "audio":
			resp.Audio = append(resp.Audio, track)
		}
	}
	if resp.Video.Codec == "" {
		return videoProbeResponse{}, errors.New("no video stream")
	}
	resp.NativeLikely = isLikelyNativeVideoProbe(resp)
	resp.RemuxSupported = isMP4CopyVideoTrack(resp.Video)
	resp.RemuxReason = remuxReason(resp)
	resp.RemuxRecommended = resp.RemuxSupported && resp.RemuxReason != ""
	return resp, nil
}

func streamTag(tags map[string]string, key string) string {
	if len(tags) == 0 {
		return ""
	}
	for k, v := range tags {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

func selectOptionalAudioIndex(r *http.Request, tracks []mkvTrack) (int, error) {
	if len(tracks) == 0 {
		return -1, nil
	}
	raw := strings.TrimSpace(r.URL.Query().Get("audio"))
	if raw != "" {
		idx, err := strconv.Atoi(raw)
		if err != nil {
			return 0, errors.New("invalid audio index")
		}
		for _, a := range tracks {
			if a.Index == idx {
				return idx, nil
			}
		}
		return 0, errors.New("audio track not found")
	}
	for _, a := range tracks {
		if a.Default {
			return a.Index, nil
		}
	}
	return tracks[0].Index, nil
}

func selectAudioIndex(r *http.Request, probe mkvProbeResponse) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("audio"))
	if raw != "" {
		idx, err := strconv.Atoi(raw)
		if err != nil {
			return 0, errors.New("invalid audio index")
		}
		for _, a := range probe.Audio {
			if a.Index == idx {
				return idx, nil
			}
		}
		return 0, errors.New("audio track not found")
	}
	for _, a := range probe.Audio {
		if a.Default {
			return a.Index, nil
		}
	}
	return probe.Audio[0].Index, nil
}

func selectSubIndex(r *http.Request, probe mkvProbeResponse) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("sub"))
	if raw == "" {
		return 0, errors.New("missing sub index")
	}
	idx, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("invalid sub index")
	}
	for _, s := range probe.Subs {
		if s.Index == idx {
			return idx, nil
		}
	}
	return 0, errors.New("subtitle track not found")
}

func codecForIndex(tracks []mkvTrack, index int) string {
	for _, t := range tracks {
		if t.Index == index {
			return strings.ToLower(t.Codec)
		}
	}
	return ""
}

func isMP4CopyAudioCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "aac":
		return true
	default:
		return false
	}
}

func isMP4CopyVideoTrack(track mkvTrack) bool {
	codec := strings.ToLower(track.Codec)
	if codec != "h264" && codec != "hevc" {
		return false
	}
	if codec == "hevc" {
		return true
	}
	if track.PixFmt != "" && !strings.EqualFold(track.PixFmt, "yuv420p") {
		return false
	}
	prof := strings.ToLower(track.Profile)
	if prof == "" {
		return true
	}
	switch prof {
	case "baseline", "main", "high", "constrained baseline":
		return true
	default:
		return false
	}
}

func isWebVTTConvertibleSubtitleCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "ass", "ssa", "subrip", "srt", "webvtt", "mov_text", "text":
		return true
	default:
		return false
	}
}

func formatHasName(formatName, want string) bool {
	for _, part := range strings.Split(strings.ToLower(formatName), ",") {
		if strings.TrimSpace(part) == want {
			return true
		}
	}
	return false
}

func isLikelyNativeVideoProbe(resp videoProbeResponse) bool {
	if formatHasName(resp.FormatName, "mov") || formatHasName(resp.FormatName, "mp4") || formatHasName(resp.FormatName, "webm") {
		return true
	}
	return false
}

func remuxReason(resp videoProbeResponse) string {
	switch {
	case formatHasName(resp.FormatName, "mpegts"):
		return "source is MPEG-TS, which browsers typically refuse as direct file playback"
	case !isLikelyNativeVideoProbe(resp):
		return "source container is not a browser-friendly direct playback format"
	default:
		return ""
	}
}

func (a *app) generateMKVPlayAsset(ctx context.Context, src, dst string, videoIndex, audioIndex int, copyVideo, copyAudio bool) error {
	tmp := dst + ".tmp.mp4"
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-i", src,
		"-map", fmt.Sprintf("0:%d", videoIndex),
		"-map", fmt.Sprintf("0:%d", audioIndex),
		"-map", "-0:s",
		"-map", "-0:d",
	}
	if !copyVideo {
		return errors.New("video transcoding disabled")
	}
	args = append(args, "-c:v", "copy")
	if copyAudio {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "192k")
	}
	args = append(args, "-map_metadata", "-1", "-movflags", "+faststart", "-f", "mp4", "-y", tmp)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(string(out)))
		}
		return err
	}
	return os.Rename(tmp, dst)
}

func generateVideoPlayAsset(ctx context.Context, src, dst, formatName string, videoIndex, audioIndex int, audioCodec string, copyAudio bool) error {
	tmp := dst + ".tmp.mp4"
	args := []string{
		"-hide_banner",
		"-loglevel", "error",
		"-fflags", "+genpts+discardcorrupt",
		"-err_detect", "ignore_err",
		"-i", src,
		"-map", fmt.Sprintf("0:%d", videoIndex),
		"-map", "-0:s",
		"-map", "-0:d",
		"-c:v", "copy",
	}
	if audioIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", audioIndex))
		if copyAudio {
			args = append(args, "-c:a", "copy")
			if strings.EqualFold(audioCodec, "aac") {
				args = append(args, "-bsf:a", "aac_adtstoasc")
			}
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "192k")
		}
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-map_metadata", "-1", "-avoid_negative_ts", "make_zero", "-movflags", "+faststart", "-f", "mp4", "-y", tmp)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(string(out)))
		}
		return err
	}
	return os.Rename(tmp, dst)
}

func generateMKVSubtitleAsset(ctx context.Context, src, dst string, subIndex int) error {
	tmp := dst + ".tmp.vtt"
	cmd := exec.CommandContext(
		ctx,
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error",
		"-i", src,
		"-map", fmt.Sprintf("0:%d", subIndex),
		"-c:s", "webvtt",
		"-f", "webvtt",
		"-y", tmp,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(string(out)))
		}
		return err
	}
	return os.Rename(tmp, dst)
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

func deriveMediaNoteOutput(note string) (string, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return "", errors.New("note is empty")
	}
	if numericNotePattern.MatchString(note) {
		value, err := strconv.ParseFloat(note, 64)
		if err != nil {
			return "", errors.New("invalid numeric note")
		}
		for value > 20 {
			value /= 10
		}
		return "mb_score:" + strconv.FormatFloat(value, 'f', -1, 64), nil
	}
	return "rakuyo_note:" + note, nil
}

func renameWithUnixPrefix(src string) (string, error) {
	if unixFilenamePattern.MatchString(filepath.Base(src)) {
		return src, nil
	}
	dir := filepath.Dir(src)
	base := strings.ReplaceAll(filepath.Base(src), "'", "_")
	unix := time.Now().Unix()
	for {
		dst := filepath.Join(dir, fmt.Sprintf("%d_%s", unix, base))
		if _, err := os.Lstat(dst); err == nil {
			unix++
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := os.Rename(src, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
}

func relWithRenamedBase(rel, base string) string {
	dir := filepath.Dir(rel)
	if dir == "." || dir == "" {
		return base
	}
	return filepath.ToSlash(filepath.Join(dir, base))
}

func tagsForPath(filePath string) []string {
	dir := filepath.ToSlash(filepath.Dir(filepath.Clean(filePath)))
	if dir == "." || dir == "/" || dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		tags = append(tags, strings.ReplaceAll(part, " ", "_"))
	}
	return tags
}

func runTag2CreateLink(ctx context.Context, filePath string, tags []string) error {
	tag2Path, err := exec.LookPath("tag2")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, tag2Path, "create-link", filePath)
	cmd.Stdin = strings.NewReader(strings.Join(tags, "\n") + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) > 0 {
			return fmt.Errorf("tag2: %s", strings.TrimSpace(string(out)))
		}
		return err
	}
	return nil
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

func generateImageThumb(ctx context.Context, src, dst string, maxSize int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	if w <= 0 || h <= 0 {
		return errors.New("invalid image dimensions")
	}

	tw, th := fitInside(w, h, maxSize)
	dstImg := image.NewRGBA(image.Rect(0, 0, tw, th))
	draw.ApproxBiLinear.Scale(dstImg, dstImg.Bounds(), img, b, draw.Over, nil)

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	return jpeg.Encode(out, dstImg, &jpeg.Options{Quality: 82})
}

func generateVideoThumb(ctx context.Context, src, dst string, maxSize int) error {
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", maxSize, maxSize)
	cmd := exec.CommandContext(
		ctx,
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
