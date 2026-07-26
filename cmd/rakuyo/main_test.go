package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

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
