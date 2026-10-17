package utils

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestSendErrorResponseEscapes(t *testing.T) {
	rec := httptest.NewRecorder()
	msg := `bad "name" \ <script>` + "\n"
	SendErrorResponse(rec, msg)
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["error"] != msg {
		t.Fatalf("not valid JSON or message changed: %q (%v)", rec.Body.String(), err)
	}
}
