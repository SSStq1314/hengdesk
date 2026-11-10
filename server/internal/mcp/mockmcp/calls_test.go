package mockmcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallsEndpointCounts(t *testing.T) {
	m := New("")
	m.record("create_ticket", map[string]any{"title": "x"})
	srv := httptest.NewServer(m.Handler())
	t.Cleanup(srv.Close)
	res, err := http.Get(srv.URL + "/calls")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	var all map[string][]map[string]any
	if err := json.NewDecoder(res.Body).Decode(&all); err != nil {
		t.Fatal(err)
	}
	if len(all["create_ticket"]) != 1 {
		t.Fatalf("calls=%v", all)
	}
}
