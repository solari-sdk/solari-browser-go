package solari

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestProfilesList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/profiles" {
			t.Errorf("request = %s %s, want GET /profiles", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"prof_1","name":"shopper"},{"id":"prof_2","name":"admin"}]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	profiles, err := c.Profiles.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []Profile{{ID: "prof_1", Name: "shopper"}, {ID: "prof_2", Name: "admin"}}
	if !reflect.DeepEqual(profiles, want) {
		t.Errorf("profiles = %+v, want %+v", profiles, want)
	}
}

func TestProfilesListEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	profiles, err := c.Profiles.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("profiles = %+v, want empty", profiles)
	}
}

func TestProfilesCreate(t *testing.T) {
	var gotMethod, gotPath string
	var body map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"prof_9","name":"shopper"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	prof, err := c.Profiles.Create(context.Background(), "shopper")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/profiles" {
		t.Errorf("request = %s %s, want POST /profiles", gotMethod, gotPath)
	}
	if body["name"] != "shopper" || len(body) != 1 {
		t.Errorf("body = %+v, want {name: shopper}", body)
	}
	if prof.ID != "prof_9" || prof.Name != "shopper" {
		t.Errorf("profile = %+v", *prof)
	}
}

// Create surfaces the gateway's error code (e.g. a plan quota).
func TestProfilesCreateErrorCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(402)
		_, _ = w.Write([]byte(`{"code":"PlanLimitExceeded","error":"profile quota reached"}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, err := c.Profiles.Create(context.Background(), "shopper")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsCode(err, CodePlanLimitExceeded) {
		t.Errorf("error = %v, want code %s", err, CodePlanLimitExceeded)
	}
}

// Delete is idempotent: a 404 is a success.
func TestProfilesDelete(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"204 succeeds", 204, false},
		{"404 is tolerated", 404, false},
		{"403 fails", 403, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			err := c.Profiles.Delete(context.Background(), "prof_1")
			if tc.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("delete: %v", err)
			}
			if gotMethod != http.MethodDelete || gotPath != "/profiles/prof_1" {
				t.Errorf("request = %s %s, want DELETE /profiles/prof_1", gotMethod, gotPath)
			}
		})
	}
}

func TestProfilesSave(t *testing.T) {
	var gotMethod, gotPath string
	var body map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":3,"sizeBytes":2048}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Profiles.Save(context.Background(), "prof_1", StorageState{
		Cookies: []Cookie{{Name: "sid", Value: "abc", Domain: ".example.com", Secure: true, SameSite: "Lax"}},
		Origins: []Origin{{Origin: "https://example.com", LocalStorage: []LocalStorageEntry{{Name: "k", Value: "v"}}}},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/profiles/prof_1/save" {
		t.Errorf("request = %s %s, want POST /profiles/prof_1/save", gotMethod, gotPath)
	}
	// The storage state is nested under a storageState key.
	state, ok := body["storageState"]
	if !ok {
		t.Fatalf("body = %+v, want a storageState key", body)
	}
	var decoded StorageState
	if err := json.Unmarshal(state, &decoded); err != nil {
		t.Fatalf("storageState is not valid: %v", err)
	}
	if len(decoded.Cookies) != 1 || decoded.Cookies[0].Name != "sid" {
		t.Errorf("cookies = %+v", decoded.Cookies)
	}
	if res.Version != 3 || res.SizeBytes != 2048 {
		t.Errorf("result = %+v, want {3 2048}", *res)
	}
}

// A response missing version/sizeBytes defaults both to 0.
func TestProfilesSaveDefaults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	res, err := c.Profiles.Save(context.Background(), "prof_1", StorageState{})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if res.Version != 0 || res.SizeBytes != 0 {
		t.Errorf("result = %+v, want zeroes", *res)
	}
}

// A storage state survives a decode → encode round trip, including top-level
// keys this SDK does not model.
func TestStorageStateRoundTripPreservesUnknownKeys(t *testing.T) {
	const raw = `{"cookies":[{"name":"sid","value":"abc","expires":-1,"sameSite":"Lax"}],"origins":[{"origin":"https://example.com","localStorage":[{"name":"k","value":"v"}]}],"futureField":{"nested":true},"version":7}`

	var state StorageState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(state.Cookies) != 1 || state.Cookies[0].Expires != -1 {
		t.Errorf("cookies = %+v", state.Cookies)
	}
	if len(state.Extra) != 2 {
		t.Errorf("Extra = %v, want futureField + version retained", state.Extra)
	}

	out, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got, want interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %s\nwant %s", out, raw)
	}
}

// The zero storage state marshals to an empty object, not null.
func TestStorageStateZeroValueMarshals(t *testing.T) {
	out, err := json.Marshal(StorageState{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `{}` {
		t.Errorf("marshal = %s, want {}", out)
	}
}
