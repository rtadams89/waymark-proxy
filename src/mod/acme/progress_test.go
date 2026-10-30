package acme

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	legolog "github.com/go-acme/lego/v5/log"
)

type progressResponse struct {
	Entries []ProgressEntry `json:"entries"`
	Next    int             `json:"next"`
	Done    bool            `json:"done"`
	Error   string          `json:"error"`
	Found   bool            `json:"found"`
}

func fetchProgress(t *testing.T, query string) progressResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	HandleObtainProgress(rec, httptest.NewRequest("GET", "/api/acme/obtainCert/progress?"+query, nil))
	var resp progressResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response %q: %v", rec.Body.String(), err)
	}
	return resp
}

func TestProgressLog(t *testing.T) {
	id := "test-progress-0001"
	AddProgress(id, "info", "step one")
	AddProgress(id, "info", "step two")

	resp := fetchProgress(t, "id="+id)
	if !resp.Found || resp.Done || len(resp.Entries) != 2 || resp.Next != 2 {
		t.Fatalf("unexpected progress: %+v", resp)
	}
	resp = fetchProgress(t, "id="+id+"&from=1")
	if len(resp.Entries) != 1 || resp.Entries[0].Message != "step two" {
		t.Fatalf("from=1 should return only the second entry: %+v", resp)
	}

	FinishProgress(id, errors.New("boom"))
	resp = fetchProgress(t, "id="+id+"&from=2")
	if !resp.Done || resp.Error != "boom" || len(resp.Entries) != 0 {
		t.Fatalf("finished progress not reported: %+v", resp)
	}
}

func TestProgressIDValidation(t *testing.T) {
	AddProgress("../bad", "info", "x")
	AddProgress("", "info", "x")
	if _, ok := progressLogs.Load("../bad"); ok {
		t.Fatal("an invalid progress ID must be ignored")
	}
	rec := httptest.NewRecorder()
	HandleObtainProgress(rec, httptest.NewRequest("GET", "/api/acme/obtainCert/progress?id=bad!", nil))
	if !json.Valid(rec.Body.Bytes()) || rec.Body.String() == "" {
		t.Fatalf("unexpected response %q", rec.Body.String())
	}
	if resp := fetchProgress(t, "id=unknown-id-123"); resp.Found {
		t.Fatal("an unknown ID must not be found")
	}
}

func TestLegoMessagesGoToActiveProgress(t *testing.T) {
	installLegoLogger(&ACMEHandler{})
	id := "test-progress-lego"
	setActiveProgress(id)
	legolog.Info("dns01: waiting for record propagation.", legolog.DomainAttr("example.com"))
	legolog.Debug("debug lines are not shown")
	setActiveProgress("")
	legolog.Info("not part of this request")

	resp := fetchProgress(t, "id="+id)
	if len(resp.Entries) != 1 {
		t.Fatalf("expected one lego entry, got %+v", resp.Entries)
	}
	want := "dns01: waiting for record propagation. (domain=example.com)"
	if resp.Entries[0].Message != want {
		t.Fatalf("got %q, want %q", resp.Entries[0].Message, want)
	}
}
