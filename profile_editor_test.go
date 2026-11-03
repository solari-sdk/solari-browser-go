package solari

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// editorGateway fakes the handoff + editor routes and checks the handoff header.
func editorGateway(t *testing.T, onEditor func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/profiles/prof_1/login-handoff":
			_, _ = w.Write([]byte(`{"handoffId":"h_1","url":"https://edit.example/h_1","expiresAt":"2030-01-01T00:30:00.000Z","version":3,"profileId":"prof_1","mode":"cold"}`))
		case r.URL.Path == "/profiles/prof_1/editor" || r.URL.Path == "/profiles/prof_1/editor/save":
			if r.Header.Get("X-Handoff-Id") != "h_1" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"missing handoff"}`))
				return
			}
			onEditor(w, r)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
}

func openEditor(t *testing.T, srv *httptest.Server) *Editor {
	t.Helper()
	ed, err := newTestClient(t, srv.URL).Profiles.Edit(context.Background(), "prof_1", "refresh login")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	return ed
}

func TestProfilesEdit(t *testing.T) {
	srv := editorGateway(t, nil)
	defer srv.Close()
	ed := openEditor(t, srv)
	if ed.URL != "https://edit.example/h_1" || ed.HandoffID != "h_1" || ed.StartVersion != 3 || ed.ProfileID != "prof_1" {
		t.Errorf("editor = %+v", ed)
	}
	if want := time.Date(2030, 1, 1, 0, 30, 0, 0, time.UTC); !ed.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", ed.ExpiresAt, want)
	}
}

func TestEditorStatusSaveExit(t *testing.T) {
	var got []string
	srv := editorGateway(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"editorStatus":"editing","version":3}`))
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"ok":true,"version":4}`))
		case http.MethodDelete:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})
	defer srv.Close()
	ed := openEditor(t, srv)
	ctx := context.Background()

	st, err := ed.Status(ctx)
	if err != nil || st.EditorStatus != EditorEditing || st.Version != 3 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if v, err := ed.SaveAndClose(ctx); err != nil || v != 4 {
		t.Fatalf("save = %d, %v", v, err)
	}
	if err := ed.Exit(ctx); err != nil {
		t.Fatalf("exit: %v", err)
	}
	want := []string{"GET /profiles/prof_1/editor", "POST /profiles/prof_1/editor/save", "DELETE /profiles/prof_1/editor"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("requests = %v, want %v", got, want)
		}
	}
}

func TestEditorStatusNullIsIdle(t *testing.T) {
	srv := editorGateway(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"prof_1","editorStatus":null,"editorTaskArn":null,"version":3}`))
	})
	defer srv.Close()
	st, err := openEditor(t, srv).Status(context.Background())
	if err != nil || st.EditorStatus != EditorIdle || st.Version != 3 {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

func TestEditorErrorStatuses(t *testing.T) {
	for _, status := range []int{404, 409, 502} {
		status := status
		var hits int32
		srv := editorGateway(t, func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
		})
		ed := openEditor(t, srv)
		_, err := ed.SaveAndClose(context.Background())
		var serr *SolariError
		if !errors.As(err, &serr) || serr.Status != status {
			t.Errorf("status %d: err = %v", status, err)
		}
		if status != 502 && hits != 1 {
			t.Errorf("status %d: %d attempts, want 1 (not retried)", status, hits)
		}
		srv.Close()
	}
}

func TestEditorWaitForSave(t *testing.T) {
	var polls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/profiles/prof_1/login-handoff" {
			_, _ = w.Write([]byte(`{"handoffId":"h_1","url":"u","expiresAt":` + "4102444800000" + `,"version":3,"profileId":"prof_1"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/profiles" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if atomic.AddInt32(&polls, 1) < 3 {
			_, _ = w.Write([]byte(`[{"id":"prof_1","name":"a","version":3}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"prof_2","name":"b","version":9},{"id":"prof_1","name":"a","version":4}]`))
	}))
	defer srv.Close()
	ed := openEditor(t, srv)
	ed.PollInterval = time.Millisecond
	v, err := ed.WaitForSave(context.Background(), 5*time.Second)
	if err != nil || v != 4 || polls != 3 {
		t.Fatalf("v=%d err=%v polls=%d", v, err, polls)
	}
}

func TestEditorWaitForSaveTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/profiles/prof_1/login-handoff" {
			_, _ = w.Write([]byte(`{"handoffId":"h_1","url":"u","expiresAt":"2030-01-01T00:00:00Z","version":3,"profileId":"prof_1"}`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":"prof_1","name":"a","version":3}]`))
	}))
	defer srv.Close()
	ed := openEditor(t, srv)
	ed.PollInterval = time.Millisecond
	_, err := ed.WaitForSave(context.Background(), 20*time.Millisecond)
	var serr *SolariError
	if !errors.As(err, &serr) || serr.Status != 408 {
		t.Fatalf("err = %v, want 408 SolariError", err)
	}
	// An already-expired link also ends the wait with 408.
	ed.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := ed.WaitForSave(context.Background(), 0); !errors.As(err, &serr) || serr.Status != 408 {
		t.Fatalf("expired: err = %v", err)
	}
}
