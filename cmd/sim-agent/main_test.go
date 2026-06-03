package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── featureID / layerID ───────────────────────────────────────────────────────

func TestFeatureID(t *testing.T) {
	cases := []struct {
		agentID, unitIdx int
		want             string
	}{
		{1, 0, "00000000-0001-0000-0000-000000000000"},
		{2, 1, "00000000-0002-0001-0000-000000000000"},
		{3, 2, "00000000-0003-0002-0000-000000000000"},
	}
	for _, tc := range cases {
		got := featureID(tc.agentID, tc.unitIdx)
		if got != tc.want {
			t.Errorf("featureID(%d,%d) = %q, want %q", tc.agentID, tc.unitIdx, got, tc.want)
		}
	}
}

func TestLayerID(t *testing.T) {
	cases := []struct {
		agentID int
		want    string
	}{
		{1, "00000000-0000-0000-0000-000000000001"},
		{2, "00000000-0000-0000-0000-000000000002"},
		{3, "00000000-0000-0000-0000-000000000003"},
	}
	for _, tc := range cases {
		got := layerID(tc.agentID)
		if got != tc.want {
			t.Errorf("layerID(%d) = %q, want %q", tc.agentID, got, tc.want)
		}
	}
}

// ── clampf ────────────────────────────────────────────────────────────────────

func TestClampf(t *testing.T) {
	if got := clampf(5, 0, 10); got != 5 {
		t.Errorf("clampf(5,0,10) = %f, want 5", got)
	}
	if got := clampf(-1, 0, 10); got != 0 {
		t.Errorf("clampf(-1,0,10) = %f, want 0", got)
	}
	if got := clampf(11, 0, 10); got != 10 {
		t.Errorf("clampf(11,0,10) = %f, want 10", got)
	}
	if got := clampf(0, 0, 10); got != 0 {
		t.Errorf("clampf(0,0,10) at lo boundary = %f, want 0", got)
	}
	if got := clampf(10, 0, 10); got != 10 {
		t.Errorf("clampf(10,0,10) at hi boundary = %f, want 10", got)
	}
}

// ── movePoint ─────────────────────────────────────────────────────────────────

func TestMovePoint_north(t *testing.T) {
	// Moving due north from equator by ~111 km should advance lat by ~1°.
	lat, lon := movePoint(0, 0, 0, 111000)
	if math.Abs(lat-1.0) > 0.02 {
		t.Errorf("bearing 0°: expected lat≈1.0°, got %.4f°", lat)
	}
	if math.Abs(lon) > 0.001 {
		t.Errorf("bearing 0°: expected lon≈0°, got %.6f°", lon)
	}
}

func TestMovePoint_east(t *testing.T) {
	// Moving due east from equator by ~111 km should advance lon by ~1°.
	lat, lon := movePoint(0, 0, 90, 111000)
	if math.Abs(lat) > 0.001 {
		t.Errorf("bearing 90°: expected lat≈0°, got %.6f°", lat)
	}
	if math.Abs(lon-1.0) > 0.02 {
		t.Errorf("bearing 90°: expected lon≈1.0°, got %.4f°", lon)
	}
}

func TestMovePoint_zero(t *testing.T) {
	lat, lon := movePoint(51.0, 10.0, 45, 0)
	if math.Abs(lat-51.0) > 1e-9 || math.Abs(lon-10.0) > 1e-9 {
		t.Errorf("zero distance: position should not change, got (%.6f,%.6f)", lat, lon)
	}
}

// ── formatDTG ─────────────────────────────────────────────────────────────────

func TestFormatDTG(t *testing.T) {
	// 2026-06-03 14:05 UTC → "031405ZJUN26"
	ts := time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC)
	got := formatDTG(ts)
	want := "031405ZJUN26"
	if got != want {
		t.Errorf("formatDTG = %q, want %q", got, want)
	}
}

func TestFormatDTG_padded(t *testing.T) {
	// 2026-01-01 00:00 UTC → "010000ZJAN26"
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := formatDTG(ts)
	want := "010000ZJAN26"
	if got != want {
		t.Errorf("formatDTG = %q, want %q", got, want)
	}
}

// ── splitURLs ─────────────────────────────────────────────────────────────────

func TestSplitURLs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"http://a:8080", []string{"http://a:8080"}},
		{"http://a:8080,http://b:8080", []string{"http://a:8080", "http://b:8080"}},
		{"  http://a:8080 , http://b:8080 ", []string{"http://a:8080", "http://b:8080"}},
		{",,,", nil},
	}
	for _, tc := range cases {
		got := splitURLs(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitURLs(%q): got %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("splitURLs(%q)[%d]: got %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// ── logRing ───────────────────────────────────────────────────────────────────

func TestLogRing_capEnforcement(t *testing.T) {
	r := newLogRing(3)
	for i := 0; i < 5; i++ {
		r.add(logEntry{Event: "e", Detail: string(rune('A' + i))})
	}
	all := r.all()
	if len(all) != 3 {
		t.Fatalf("expected 3 entries (cap), got %d", len(all))
	}
	// Should retain the last 3: C, D, E
	for i, want := range []string{"C", "D", "E"} {
		if all[i].Detail != want {
			t.Errorf("entry[%d].Detail = %q, want %q", i, all[i].Detail, want)
		}
	}
}

func TestLogRing_allReturnsCopy(t *testing.T) {
	r := newLogRing(10)
	r.add(logEntry{Event: "x"})
	a := r.all()
	a[0].Event = "modified"
	b := r.all()
	if b[0].Event == "modified" {
		t.Error("all() returned a reference to internal buffer, not a copy")
	}
}

func TestLogRing_empty(t *testing.T) {
	r := newLogRing(10)
	if got := r.all(); len(got) != 0 {
		t.Errorf("empty ring: expected [], got %v", got)
	}
}

// ── newAgent / scenario fallback ──────────────────────────────────────────────

func TestNewAgent_unknownScenarioFallsBack(t *testing.T) {
	cfg := config{agentID: 1, scenario: "nonexistent"}
	a := newAgent(cfg)
	if a.scen.name != "Central Europe" {
		t.Errorf("unknown scenario should fall back to Central Europe, got %q", a.scen.name)
	}
}

func TestNewAgent_allScenarios(t *testing.T) {
	for id, scen := range scenarioMap {
		cfg := config{agentID: 1, scenario: id}
		a := newAgent(cfg)
		if a.scen != scen {
			t.Errorf("scenario %q: got wrong scenario %q", id, a.scen.name)
		}
	}
}

func TestNewAgent_unitsWithinBounds(t *testing.T) {
	for id := range scenarioMap {
		cfg := config{agentID: 1, scenario: id}
		a := newAgent(cfg)
		b := a.scen.bounds
		for i, u := range a.units {
			if u.lat < b.minLat || u.lat > b.maxLat {
				t.Errorf("scenario %q unit %d lat %.4f outside [%.1f,%.1f]", id, i, u.lat, b.minLat, b.maxLat)
			}
			if u.lon < b.minLon || u.lon > b.maxLon {
				t.Errorf("scenario %q unit %d lon %.4f outside [%.1f,%.1f]", id, i, u.lon, b.minLon, b.maxLon)
			}
		}
	}
}

// ── generateADP ───────────────────────────────────────────────────────────────

func newTestAgent() *agent {
	cfg := config{agentID: 1, scenario: "central-europe"}
	return newAgent(cfg)
}

func TestGenerateADP_messageCount(t *testing.T) {
	a := newTestAgent()
	// Non-ORBAT cycle: 3 OWNSITREP + 2 SITREP + 2 SPOTREP + 1 LOGREP + 1 SPOTREP = 9
	msgs := a.generateADP(1)
	if len(msgs) != 9 {
		t.Errorf("non-ORBAT cycle: expected 9 messages, got %d", len(msgs))
	}
	// ORBAT cycle (multiple of 5): 3 + 2 + 2 + 1 + 1 ORBAT = 9
	msgs5 := a.generateADP(5)
	if len(msgs5) != 9 {
		t.Errorf("ORBAT cycle: expected 9 messages, got %d", len(msgs5))
	}
}

func TestGenerateADP_allHaveMSGIDandENDREC(t *testing.T) {
	a := newTestAgent()
	for _, cycle := range []int{1, 5, 10, 13} {
		for i, msg := range a.generateADP(cycle) {
			if !strings.HasPrefix(msg, "MSGID/") {
				t.Errorf("cycle %d msg[%d]: missing MSGID prefix", cycle, i)
			}
			if !strings.HasSuffix(msg, "ENDREC/") {
				t.Errorf("cycle %d msg[%d]: missing ENDREC suffix", cycle, i)
			}
		}
	}
}

func TestGenerateADP_orBATOnEvery5thCycle(t *testing.T) {
	a := newTestAgent()
	for cycle := 1; cycle <= 20; cycle++ {
		msgs := a.generateADP(cycle)
		hasORBAT := false
		for _, m := range msgs {
			if strings.HasPrefix(m, "MSGID/ORBAT/") {
				hasORBAT = true
			}
		}
		if cycle%5 == 0 && !hasORBAT {
			t.Errorf("cycle %d should contain ORBAT message", cycle)
		}
		if cycle%5 != 0 && hasORBAT {
			t.Errorf("cycle %d should NOT contain ORBAT message", cycle)
		}
	}
}

func TestGenerateADP_ownsitrepsHaveCoords(t *testing.T) {
	a := newTestAgent()
	msgs := a.generateADP(1)
	ownsitreps := 0
	for _, m := range msgs {
		if strings.HasPrefix(m, "MSGID/OWNSITREP/") {
			ownsitreps++
			if !strings.Contains(m, "LOCATION/WGS84/") {
				t.Errorf("OWNSITREP missing LOCATION/WGS84: %s", m)
			}
		}
	}
	if ownsitreps != 3 {
		t.Errorf("expected 3 OWNSITREPs, got %d", ownsitreps)
	}
}

// ── Control API ───────────────────────────────────────────────────────────────

func newTestMux() (*agent, *http.ServeMux) {
	cfg := config{
		agentID:  1,
		scenario: "central-europe",
		interval: time.Second,
		// empty peer lists so step() makes no HTTP calls
	}
	a := newAgent(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sim/status", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		resp := map[string]any{
			"agent": a.cfg.agentID, "scenario": a.scen.name,
			"running": a.running, "cycle": a.cycle, "lastErr": a.lastErr,
			"stats": map[string]any{},
			"units": []any{},
		}
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("POST /sim/start", func(w http.ResponseWriter, r *http.Request) {
		a.start(); w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /sim/stop", func(w http.ResponseWriter, r *http.Request) {
		a.stop(); w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /sim/step", func(w http.ResponseWriter, r *http.Request) {
		go a.step(); w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /sim/reset", func(w http.ResponseWriter, r *http.Request) {
		a.stop()
		a.mu.Lock()
		a.cycle = 0
		a.lastErr = ""
		a.stats = [4]deliveryStat{}
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /sim/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.log.all())
	})
	return a, mux
}

func TestControlAPI_status(t *testing.T) {
	_, mux := newTestMux()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sim/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["agent"].(float64) != 1 {
		t.Errorf("agent: got %v want 1", resp["agent"])
	}
	if resp["running"].(bool) {
		t.Error("agent should not be running yet")
	}
}

func TestControlAPI_startStop(t *testing.T) {
	a, mux := newTestMux()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sim/start", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("start: got %d want 204", w.Code)
	}
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if !running {
		t.Error("agent should be running after start")
	}

	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sim/stop", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("stop: got %d want 204", w.Code)
	}
	a.mu.Lock()
	running = a.running
	a.mu.Unlock()
	if running {
		t.Error("agent should not be running after stop")
	}
}

func TestControlAPI_step(t *testing.T) {
	_, mux := newTestMux()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sim/step", nil))
	if w.Code != http.StatusAccepted {
		t.Fatalf("step: got %d want 202", w.Code)
	}
}

func TestControlAPI_reset(t *testing.T) {
	a, mux := newTestMux()
	// Advance cycle manually
	a.mu.Lock()
	a.cycle = 42
	a.stats[0].sent = 10
	a.mu.Unlock()

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/sim/reset", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("reset: got %d want 204", w.Code)
	}
	a.mu.Lock()
	cycle := a.cycle
	sent := a.stats[0].sent
	a.mu.Unlock()
	if cycle != 0 {
		t.Errorf("cycle should be 0 after reset, got %d", cycle)
	}
	if sent != 0 {
		t.Errorf("stats should be cleared after reset, got sent=%d", sent)
	}
}

func TestControlAPI_log(t *testing.T) {
	a, mux := newTestMux()
	a.log.add(logEntry{Event: "test", Agent: 1})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sim/log", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("log: got %d want 200", w.Code)
	}
	var entries []logEntry
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 1 || entries[0].Event != "test" {
		t.Errorf("expected 1 entry with event=test, got %v", entries)
	}
}

// ── moveUnit stays in bounds ──────────────────────────────────────────────────

func TestMoveUnit_staysInBounds(t *testing.T) {
	cfg := config{agentID: 1, scenario: "central-europe"}
	a := newAgent(cfg)
	b := a.scen.bounds
	for i := 0; i < 200; i++ {
		a.moveUnit(i % 3)
		for j, u := range a.units {
			if u.lat < b.minLat || u.lat > b.maxLat {
				t.Fatalf("step %d unit %d lat %.4f escaped bounds [%.1f,%.1f]", i, j, u.lat, b.minLat, b.maxLat)
			}
			if u.lon < b.minLon || u.lon > b.maxLon {
				t.Fatalf("step %d unit %d lon %.4f escaped bounds [%.1f,%.1f]", i, j, u.lon, b.minLon, b.maxLon)
			}
		}
	}
}
