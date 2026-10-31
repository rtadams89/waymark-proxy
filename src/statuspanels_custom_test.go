package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"imuslab.com/zoraxy/mod/database"
)

func TestStatusPanelLayout(t *testing.T) {
	db, err := database.NewDatabase(filepath.Join(t.TempDir(), "sys.db"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Nothing saved yet: empty layout, not null
	got := readStatusPanelLayout(db)
	if got.Visible == nil || got.Order == nil || len(got.Visible) != 0 || len(got.Order) != 0 {
		t.Fatalf("unexpected empty layout: %+v", got)
	}

	if msg := saveStatusPanelLayout(db, `{"flowDiagram":false,"cardCountries":true}`, `["systemResources","flowDiagram"]`); msg != "" {
		t.Fatal(msg)
	}
	got = readStatusPanelLayout(db)
	if !reflect.DeepEqual(got.Visible, map[string]bool{"flowDiagram": false, "cardCountries": true}) {
		t.Fatalf("visibility not saved: %+v", got.Visible)
	}
	if !reflect.DeepEqual(got.Order, []string{"systemResources", "flowDiagram"}) {
		t.Fatalf("order not saved: %+v", got.Order)
	}

	// Saving only the order keeps the visibility
	if msg := saveStatusPanelLayout(db, "", `["flowDiagram"]`); msg != "" {
		t.Fatal(msg)
	}
	got = readStatusPanelLayout(db)
	if len(got.Visible) != 2 || !reflect.DeepEqual(got.Order, []string{"flowDiagram"}) {
		t.Fatalf("order-only save changed the visibility: %+v", got)
	}
}

func TestStatusPanelLayoutRejectsBadInput(t *testing.T) {
	db, err := database.NewDatabase(filepath.Join(t.TempDir(), "sys.db"), database.GetRecommendedBackendType())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	bad := []struct{ panels, order string }{
		{"", ""},
		{`{"bad id!":true}`, ""},
		{`[]`, ""},
		{`null`, ""},
		{"", `["a","a"]`},
		{"", `["../x"]`},
		{"", `{"a":1}`},
		{"", `null`},
		// A bad order must not save good visibility either
		{`{"flowDiagram":false}`, `["x y"]`},
	}
	for _, b := range bad {
		if msg := saveStatusPanelLayout(db, b.panels, b.order); msg == "" {
			t.Errorf("panels=%q order=%q was accepted", b.panels, b.order)
		}
	}
	if got := readStatusPanelLayout(db); len(got.Visible) != 0 || len(got.Order) != 0 {
		t.Fatalf("rejected input was saved: %+v", got)
	}

	many := "["
	for i := 0; i <= maxStatusPanelCount; i++ {
		if i > 0 {
			many += ","
		}
		many += `"p` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"`
	}
	many += "]"
	if msg := saveStatusPanelLayout(db, "", many); msg == "" {
		t.Error("an order longer than the limit was accepted")
	}
}
