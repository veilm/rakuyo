package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateImageThumbFromWebP(t *testing.T) {
	const webPBase64 = "UklGRjwAAABXRUJQVlA4IDAAAADQAQCdASoCAAIAAgA0JaACdLoB+AADsAD+8Oj3/yC5YXXI1/8gP+QH/ID/+PIAAAA="

	src := filepath.Join(t.TempDir(), "source.webp")
	dst := filepath.Join(t.TempDir(), "thumb.jpg")
	data, err := base64.StdEncoding.DecodeString(webPBase64)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := generateImageThumb(context.Background(), src, dst, 256); err != nil {
		t.Fatalf("generate WebP thumbnail: %v", err)
	}
	if info, err := os.Stat(dst); err != nil {
		t.Fatalf("stat generated thumbnail: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("generated thumbnail is empty")
	}
}

func TestHandleFilePreservesFilenameForBrowserSave(t *testing.T) {
	rootDir := t.TempDir()
	filename := "host image.webp"
	if err := os.WriteFile(filepath.Join(rootDir, filename), []byte("image data"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := &app{roots: []rootMount{{ID: 0, Path: rootDir, Real: rootDir}}}
	req := httptest.NewRequest(http.MethodGet, "/api/file?root=0&path="+url.QueryEscape(filename), nil)
	rec := httptest.NewRecorder()

	a.handleFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET returned %d: %s", rec.Code, rec.Body.String())
	}
	disposition, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition: %v", err)
	}
	if disposition != "inline" {
		t.Errorf("disposition = %q, want inline", disposition)
	}
	if got := params["filename"]; got != filename {
		t.Errorf("filename = %q, want %q", got, filename)
	}
}

func TestConfigReportsMarkingCapability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			a := &app{markingEnabled: enabled}
			rec := httptest.NewRecorder()
			a.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("GET returned %d: %s", rec.Code, rec.Body.String())
			}
			var config struct {
				Marking bool `json:"marking"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &config); err != nil {
				t.Fatal(err)
			}
			if config.Marking != enabled {
				t.Errorf("marking = %v, want %v", config.Marking, enabled)
			}
		})
	}
}

func TestBackendDataPersists(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "rakuyo", "state.json")
	a := &app{
		dataStorage: "backend",
		dataPath:    statePath,
		data:        newPersistentData(),
	}

	patches := []string{
		`{"section":"fileColors","key":"0:video.mp4","value":"green"}`,
		`{"section":"playbackPositions","key":"0:video.mp4","value":42.5}`,
		`{"section":"choiceMemory","key":"video-mode:native|remux","value":"remux"}`,
	}
	for _, body := range patches {
		req := httptest.NewRequest(http.MethodPatch, "/api/data", strings.NewReader(body))
		rec := httptest.NewRecorder()
		a.handleData(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH returned %d: %s", rec.Code, rec.Body.String())
		}
	}

	reloaded := &app{dataPath: statePath, data: newPersistentData()}
	if err := reloaded.loadData(); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.data.FileColors["0:video.mp4"]; got != "green" {
		t.Errorf("file color = %q, want green", got)
	}
	if got := reloaded.data.PlaybackPositions["0:video.mp4"]; got != 42.5 {
		t.Errorf("playback position = %v, want 42.5", got)
	}
	if got := reloaded.data.ChoiceMemory["video-mode:native|remux"]; got != "remux" {
		t.Errorf("remembered choice = %q, want remux", got)
	}
}

func TestBackendDataPatchDelete(t *testing.T) {
	a := &app{
		dataStorage: "backend",
		dataPath:    filepath.Join(t.TempDir(), "state.json"),
		data:        newPersistentData(),
	}
	a.data.FileColors["0:file"] = "red"

	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/data",
		strings.NewReader(`{"section":"fileColors","key":"0:file","value":null}`),
	)
	rec := httptest.NewRecorder()
	a.handleData(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH returned %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := a.data.FileColors["0:file"]; ok {
		t.Error("file color was not deleted")
	}
}

func TestFrontendDataResponseDoesNotExposeBackendState(t *testing.T) {
	a := &app{
		dataStorage: "frontend",
		data:        newPersistentData(),
	}
	a.data.FileColors["0:file"] = "red"

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	rec := httptest.NewRecorder()
	a.handleData(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET returned %d: %s", rec.Code, rec.Body.String())
	}

	var response map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response["data"]; ok {
		t.Error("frontend response unexpectedly includes backend data")
	}
}

func TestDataPatchValidation(t *testing.T) {
	a := &app{
		dataStorage: "backend",
		dataPath:    filepath.Join(t.TempDir(), "state.json"),
		data:        newPersistentData(),
	}
	req := httptest.NewRequest(
		http.MethodPatch,
		"/api/data",
		strings.NewReader(`{"section":"fileColors","key":"0:file","value":"purple"}`),
	)
	rec := httptest.NewRecorder()
	a.handleData(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PATCH returned %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
