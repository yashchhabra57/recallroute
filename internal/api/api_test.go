package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/yashchhabra57/recallroute/internal/service"
	"github.com/yashchhabra57/recallroute/internal/store/memstore"
)

type client struct {
	t     *testing.T
	srv   *httptest.Server
	token string
}

func newClient(t *testing.T, token string) *client {
	t.Helper()
	svc := service.New(memstore.New(), nil)
	srv := httptest.NewServer(New(svc, Config{Token: token, StoreKind: "memory"}))
	t.Cleanup(srv.Close)
	return &client{t: t, srv: srv, token: token}
}

func (c *client) do(method, path, ctype string, body io.Reader, out any) int {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.srv.URL+path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			c.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (c *client) file(method, path, ctype, name string, out any) int {
	c.t.Helper()
	f, err := os.Open("../../testdata/" + name)
	if err != nil {
		c.t.Fatal(err)
	}
	defer f.Close()
	return c.do(method, path, ctype, f, out)
}

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

type alertResp struct {
	ID       int64    `json:"id"`
	RecallID string   `json:"recall_id"`
	Tier     string   `json:"tier"`
	State    string   `json:"state"`
	Version  int      `json:"version"`
	Actions  []string `json:"actions"`
	Lot      struct {
		SKU      string `json:"sku"`
		Quantity int    `json:"quantity"`
	} `json:"lot"`
	Events []struct {
		Action string `json:"action"`
	} `json:"events"`
}

type errResp struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func TestFullFlow(t *testing.T) {
	c := newClient(t, "")

	var health map[string]any
	if c.do("GET", "/healthz", "", nil, &health) != 200 || health["store"] != "memory" {
		t.Fatalf("health = %v", health)
	}

	var imp struct {
		Imported  int   `json:"imported"`
		NewAlerts []any `json:"new_alerts"`
		RowErrors []any `json:"row_errors"`
	}
	if code := c.file("POST", "/api/lots", "text/csv", "inventory_sample.csv", &imp); code != 200 || imp.Imported != 9 || len(imp.RowErrors) != 0 {
		t.Fatalf("import: %d %+v", code, imp)
	}

	var ing service.IngestSummary
	if code := c.file("POST", "/api/recalls/ingest", "application/json", "openfda_sample.json", &ing); code != 200 || len(ing.NewAlerts) != 5 {
		t.Fatalf("ingest: %d %+v", code, ing)
	}

	var alerts []alertResp
	c.do("GET", "/api/alerts?state=new", "", nil, &alerts)
	if len(alerts) != 5 {
		t.Fatalf("alerts = %d", len(alerts))
	}
	var crackers alertResp
	for _, a := range alerts {
		if a.Lot.SKU == "CRK-8" {
			crackers = a
		}
	}
	if crackers.ID == 0 || crackers.Tier != "confirmed" {
		t.Fatalf("crackers alert = %+v", crackers)
	}

	path := "/api/alerts/" + itoa(crackers.ID) + "/transitions"
	var v alertResp
	if code := c.do("POST", path, "application/json", jsonBody(map[string]any{"version": 1, "action": "acknowledge", "actor": "sam"}), &v); code != 200 || v.State != "acknowledged" {
		t.Fatalf("ack: %d %+v", code, v)
	}
	if code := c.do("POST", path, "application/json", jsonBody(map[string]any{"version": v.Version, "action": "pull", "actor": "sam", "quantity": 60}), &v); code != 200 || v.Lot.Quantity != 0 {
		t.Fatalf("pull: %d %+v", code, v)
	}

	var e errResp
	if code := c.do("POST", path, "application/json", jsonBody(map[string]any{"version": 1, "action": "notify", "actor": "sam", "note": "x"}), &e); code != 409 || e.Code != "version_conflict" {
		t.Errorf("stale version: %d %+v", code, e)
	}
	if code := c.do("POST", path, "application/json", jsonBody(map[string]any{"version": v.Version, "action": "notify", "actor": "sam"}), &e); code != 422 || e.Code != "validation" {
		t.Errorf("notify without recipients: %d %+v", code, e)
	}
	if code := c.do("POST", path, "application/json", jsonBody(map[string]any{"version": v.Version, "action": "close", "actor": "sam"}), &e); code != 409 || e.Code != "invalid_transition" {
		t.Errorf("close from pulled: %d %+v", code, e)
	}

	var full alertResp
	c.do("GET", "/api/alerts/"+itoa(crackers.ID), "", nil, &full)
	if len(full.Events) != 2 || full.State != "pulled" || strings.Join(full.Actions, ",") != "pull,notify" {
		t.Errorf("detail = %+v", full)
	}
}

func TestErrors(t *testing.T) {
	c := newClient(t, "")
	var e errResp
	cases := []struct {
		method, path, ctype, body string
		code                      int
		errCode                   string
	}{
		{"GET", "/api/alerts/999", "", "", 404, "not_found"},
		{"GET", "/api/alerts/abc", "", "", 400, "bad_request"},
		{"GET", "/api/alerts?state=bogus", "", "", 400, "bad_request"},
		{"POST", "/api/recalls/ingest", "application/json", "{nope", 400, "bad_feed"},
		{"POST", "/api/lots", "text/plain", "x", 415, "unsupported_media_type"},
		{"POST", "/api/lots", "text/csv", "sku\nA\n", 400, "bad_csv"},
		{"POST", "/api/lots", "application/json", `[{"name":"","quantity":1}]`, 422, "validation"},
		{"POST", "/api/alerts/1/transitions", "application/json", `{"action":"acknowledge"}`, 400, "bad_request"},
		{"POST", "/api/alerts/1/transitions", "application/json", `{"version":1,"action":"acknowledge","actor":"x","bogus":1}`, 400, "bad_json"},
		{"POST", "/api/alerts/1/transitions", "application/json", `{"version":1,"action":"acknowledge","actor":"x"}`, 404, "not_found"},
	}
	for _, tc := range cases {
		e = errResp{}
		code := c.do(tc.method, tc.path, tc.ctype, strings.NewReader(tc.body), &e)
		if code != tc.code || e.Code != tc.errCode {
			t.Errorf("%s %s: got %d %+v, want %d %s", tc.method, tc.path, code, e, tc.code, tc.errCode)
		}
	}
}

func TestTokenRequiredForWrites(t *testing.T) {
	c := newClient(t, "s3cret")
	anon := &client{t: t, srv: c.srv}

	var e errResp
	if code := anon.file("POST", "/api/lots", "text/csv", "inventory_sample.csv", &e); code != 401 || e.Code != "unauthorized" {
		t.Errorf("anon write: %d %+v", code, e)
	}
	wrong := &client{t: t, srv: c.srv, token: "nope"}
	if code := wrong.file("POST", "/api/lots", "text/csv", "inventory_sample.csv", &e); code != 401 {
		t.Errorf("wrong token write: %d", code)
	}
	var lots []any
	if code := anon.do("GET", "/api/lots", "", nil, &lots); code != 200 {
		t.Errorf("anon read: %d", code)
	}
	var imp map[string]any
	if code := c.file("POST", "/api/lots", "text/csv", "inventory_sample.csv", &imp); code != 200 {
		t.Errorf("authorized write: %d", code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
