// sim-agent — OrbitalC2Core simulation agent.
//
// Generates random unit movement, posts tactical symbols via the Feature API
// to peer orbital-node instances, and injects ADATP-3 messages via the
// ADATP-3 adapters.  Exposes a control REST API on SIM_LISTEN.
//
// Environment variables:
//
//	AGENT_ID          Agent identity: 1, 2 or 3
//	OWN_ORBITAL_URL   Base URL of this agent's own orbital-node
//	ALL_ORBITAL_URLS  Comma-sep URLs of all 3 nodes (for layer setup)
//	PEER_ORBITAL_URLS Comma-sep URLs of the 2 peer nodes (for feature posting)
//	PEER_ADATP3_URLS  Comma-sep ADATP-3 adapter URLs of the 2 peers
//	SCENARIO          Scenario profile (default: central-europe)
//	SIM_INTERVAL      Seconds between cycles (default: 10)
//	SIM_BURST         Messages per cycle (default: 10)
//	SIM_AUTOSTART     Start loop on boot (default: true)
//	SIM_LISTEN        Control API address (default: :9200)
//	STARTUP_TIMEOUT   Seconds to wait for deps (default: 60)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Scenario profiles ─────────────────────────────────────────────────────────

type bbox struct{ minLat, maxLat, minLon, maxLon float64 }

type unitDef struct {
	name    string
	sidc    string // 15-char APP-6 SIDC
	side    string // "FRIENDLY", "HOSTILE"
	echelon string
	equip   string
}

type scenario struct {
	name      string
	center    [2]float64 // [lat, lon]
	bounds    bbox
	maxSpeedM float64
	defs      [3]unitDef
}

var scenarioMap = map[string]*scenario{
	"central-europe": {
		name: "Central Europe", center: [2]float64{51.163375, 10.447683},
		bounds: bbox{47, 55, 6, 15}, maxSpeedM: 2000,
		defs: [3]unitDef{
			{name: "1PzGrenBtl212", sidc: "SFGPUCD--------", side: "FRIENDLY", echelon: "BATTALION", equip: "3XIFV,2XHMMWV"},
			{name: "PzBtl203",      sidc: "SFGPUCAT-------", side: "FRIENDLY", echelon: "BATTALION", equip: "14XMBT,2XARV"},
			{name: "OPFOR-Mot-1",   sidc: "SHGPUCF--------", side: "HOSTILE",  echelon: "COMPANY",   equip: "8XBMP"},
		},
	},
	"north-sea": {
		name: "North Sea", center: [2]float64{54.18, 7.89},
		bounds: bbox{53, 56, 7, 12}, maxSpeedM: 3000,
		defs: [3]unitDef{
			{name: "MarBtl1",    sidc: "SFGPUCI--------", side: "FRIENDLY", echelon: "BATTALION", equip: "3XMPV"},
			{name: "KpFla-1",    sidc: "SFGPUAD--------", side: "FRIENDLY", echelon: "COMPANY",   equip: "2XFLAK"},
			{name: "OPFOR-Coast",sidc: "SHGPUCF--------", side: "HOSTILE",  echelon: "PLATOON",   equip: "4XBOAT"},
		},
	},
	"baltic": {
		name: "Baltic", center: [2]float64{56.1, 20.0},
		bounds: bbox{54, 58, 15, 25}, maxSpeedM: 2500,
		defs: [3]unitDef{
			{name: "NATO-BG-1",   sidc: "SFGPUCI--------", side: "FRIENDLY", echelon: "BATTALION", equip: "6XIFV"},
			{name: "NATO-Art-1",  sidc: "SFGPUUA--------", side: "FRIENDLY", echelon: "BATTALION", equip: "8XSPH"},
			{name: "OPFOR-Arm-1", sidc: "SHGPUCAT-------", side: "HOSTILE",  echelon: "COMPANY",   equip: "10XMBT"},
		},
	},
	"alpine": {
		name: "Alpine", center: [2]float64{47.2, 12.0},
		bounds: bbox{46, 48, 9, 15}, maxSpeedM: 800,
		defs: [3]unitDef{
			{name: "GebJgBtl231", sidc: "SFGPUCL--------", side: "FRIENDLY", echelon: "BATTALION", equip: "3XIFV"},
			{name: "Pi-Kp-5",     sidc: "SFGPUCI--------", side: "FRIENDLY", echelon: "COMPANY",   equip: "2XAEV"},
			{name: "OPFOR-Mtn-1", sidc: "SHGPUCF--------", side: "HOSTILE",  echelon: "COMPANY",   equip: "4XBMP"},
		},
	},
}

// ── Unit state ────────────────────────────────────────────────────────────────

type unit struct {
	unitDef
	lat, lon float64
	bearing  float64
	strength int
	status   string
}

// featureID returns a deterministic UUID for this unit on a peer.
// Format: 00000000-00AA-00BB-0000-000000000000 where AA=agentID BB=unitIdx.
func featureID(agentID, unitIdx int) string {
	return fmt.Sprintf("00000000-%04d-%04d-0000-000000000000", agentID, unitIdx)
}

// layerID returns the deterministic layer UUID for an agent.
func layerID(agentID int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", agentID)
}

// movePoint moves lat/lon by bearing (degrees) and distance (metres).
func movePoint(lat, lon, bearing, distM float64) (float64, float64) {
	const r = 6371000.0
	lat1 := lat * math.Pi / 180
	lon1 := lon * math.Pi / 180
	b := bearing * math.Pi / 180
	d := distM / r
	lat2 := math.Asin(math.Sin(lat1)*math.Cos(d) + math.Cos(lat1)*math.Sin(d)*math.Cos(b))
	lon2 := lon1 + math.Atan2(math.Sin(b)*math.Sin(d)*math.Cos(lat1), math.Cos(d)-math.Sin(lat1)*math.Sin(lat2))
	return lat2 * 180 / math.Pi, lon2 * 180 / math.Pi
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ── Config ────────────────────────────────────────────────────────────────────

type config struct {
	agentID        int
	ownOrbitalURL  string
	allOrbitalURLs []string
	peerOrbitalURLs []string
	peerADATP3URLs  []string
	scenario       string
	interval       time.Duration
	burst          int
	autostart      bool
	listen         string
	startupTimeout time.Duration
}

func parseConfig() config {
	agentID, _ := strconv.Atoi(envOr("AGENT_ID", "1"))
	interval, _ := strconv.Atoi(envOr("SIM_INTERVAL", "10"))
	burst, _ := strconv.Atoi(envOr("SIM_BURST", "10"))
	timeout, _ := strconv.Atoi(envOr("STARTUP_TIMEOUT", "60"))
	return config{
		agentID:         agentID,
		ownOrbitalURL:   envOr("OWN_ORBITAL_URL", "http://localhost:8080"),
		allOrbitalURLs:  splitURLs(envOr("ALL_ORBITAL_URLS", "")),
		peerOrbitalURLs: splitURLs(envOr("PEER_ORBITAL_URLS", "")),
		peerADATP3URLs:  splitURLs(envOr("PEER_ADATP3_URLS", "")),
		scenario:        envOr("SCENARIO", "central-europe"),
		interval:        time.Duration(interval) * time.Second,
		burst:           burst,
		autostart:       envOr("SIM_AUTOSTART", "true") == "true",
		listen:          envOr("SIM_LISTEN", ":9200"),
		startupTimeout:  time.Duration(timeout) * time.Second,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitURLs(s string) []string {
	var out []string
	for _, u := range strings.Split(s, ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// ── Log ring buffer ───────────────────────────────────────────────────────────

type logEntry struct {
	Time   string `json:"time"`
	Agent  int    `json:"agent"`
	Event  string `json:"event"`
	Detail string `json:"detail,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
}

type logRing struct {
	mu  sync.Mutex
	buf []logEntry
	cap int
}

func newLogRing(cap int) *logRing { return &logRing{cap: cap} }

func (r *logRing) add(e logEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, e)
	if len(r.buf) > r.cap {
		r.buf = r.buf[len(r.buf)-r.cap:]
	}
}

func (r *logRing) all() []logEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]logEntry, len(r.buf))
	copy(out, r.buf)
	return out
}

// ── Agent ─────────────────────────────────────────────────────────────────────

type deliveryStat struct {
	sent   int
	errors int
}

type agent struct {
	cfg   config
	scen  *scenario
	units [3]*unit
	rng   *rand.Rand
	log   *logRing
	http  *http.Client

	mu      sync.Mutex
	running bool
	cycle   int
	stats   [4]deliveryStat // index 0=peer0 orbital, 1=peer1 orbital, 2=peer0 adatp3, 3=peer1 adatp3
	lastErr string
}

func newAgent(cfg config) *agent {
	scen, ok := scenarioMap[cfg.scenario]
	if !ok {
		scen = scenarioMap["central-europe"]
	}
	rng := rand.New(rand.NewSource(int64(cfg.agentID) * 0xDEADBEEF))
	b := scen.bounds
	units := [3]*unit{}
	for i := 0; i < 3; i++ {
		def := scen.defs[i]
		// Shift initial position based on agentID so agents start at different spots
		latOffset := float64(cfg.agentID-1) * 0.5
		lonOffset := float64(i) * 0.3
		units[i] = &unit{
			unitDef:  def,
			lat:      clampf(b.minLat+(b.maxLat-b.minLat)*0.3+latOffset+rng.Float64()*0.5, b.minLat, b.maxLat),
			lon:      clampf(b.minLon+(b.maxLon-b.minLon)*0.3+lonOffset+rng.Float64()*0.5, b.minLon, b.maxLon),
			bearing:  rng.Float64() * 360,
			strength: 20 + rng.Intn(15),
			status:   "OP",
		}
	}
	return &agent{
		cfg:   cfg,
		scen:  scen,
		units: units,
		rng:   rng,
		log:   newLogRing(100),
		http:  &http.Client{Timeout: 10 * time.Second},
	}
}

// ── Layer setup ───────────────────────────────────────────────────────────────

// ensureLayer creates or updates the agent's named layer on the given node.
// Uses a deterministic UUID so it is idempotent across restarts.
func (a *agent) ensureLayer(ctx context.Context, nodeURL string) error {
	lID := layerID(a.cfg.agentID)
	layerName := fmt.Sprintf("Sim-Agent-%d", a.cfg.agentID)
	body := map[string]any{
		"id":      lID,
		"name":    layerName,
		"visible": true,
	}
	_, err := a.httpPost(ctx, nodeURL+"/v1/layers", body)
	return err
}

// setupLayers creates the agent's layer on ALL orbital nodes.
func (a *agent) setupLayers(ctx context.Context) {
	for _, u := range a.cfg.allOrbitalURLs {
		if err := a.ensureLayer(ctx, u); err != nil {
			slog.Warn("layer setup failed", "url", u, "err", err)
		}
	}
	// Also push map center to own node
	center := a.scen.center
	body := map[string]float64{"latDeg": center[0], "lonDeg": center[1]}
	if _, err := a.httpPost(ctx, a.cfg.ownOrbitalURL+"/v1/map/center", body); err != nil {
		slog.Warn("map center push failed", "err", err)
	}
}

// ── Simulation loop ───────────────────────────────────────────────────────────

func (a *agent) start() {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return
	}
	a.running = true
	a.mu.Unlock()
	go a.loop()
}

func (a *agent) stop() {
	a.mu.Lock()
	a.running = false
	a.mu.Unlock()
}

func (a *agent) loop() {
	slog.Info("simulation loop started", "agent", a.cfg.agentID, "interval", a.cfg.interval)
	for {
		a.mu.Lock()
		running := a.running
		a.mu.Unlock()
		if !running {
			return
		}
		a.step()
		time.Sleep(a.cfg.interval)
	}
}

func (a *agent) step() {
	a.mu.Lock()
	a.cycle++
	cycle := a.cycle
	a.mu.Unlock()

	a.log.add(logEntry{Time: now(), Agent: a.cfg.agentID, Event: "cycle_start", Detail: fmt.Sprintf("cycle=%d", cycle)})

	// Move units
	a.moveUnits()

	ctx := context.Background()

	// Post features to peer orbital nodes
	for i, peerURL := range a.cfg.peerOrbitalURLs {
		err := a.postFeatures(ctx, peerURL)
		a.mu.Lock()
		if err != nil {
			a.stats[i].errors++
			a.lastErr = err.Error()
			a.mu.Unlock()
			ok := false
			a.log.add(logEntry{Time: now(), Agent: a.cfg.agentID, Event: "features", Detail: peerURL, OK: &ok})
		} else {
			a.stats[i].sent += len(a.units)
			a.mu.Unlock()
			ok := true
			a.log.add(logEntry{Time: now(), Agent: a.cfg.agentID, Event: "features", Detail: peerURL, OK: &ok})
		}
	}

	// Generate and post ADATP-3 messages to peer adapters
	msgs := a.generateADATP3(cycle)
	for i, adatp3URL := range a.cfg.peerADATP3URLs {
		err := a.postADATP3(ctx, adatp3URL, msgs)
		a.mu.Lock()
		if err != nil {
			a.stats[i+2].errors++
			a.lastErr = err.Error()
			a.mu.Unlock()
			ok := false
			a.log.add(logEntry{Time: now(), Agent: a.cfg.agentID, Event: "adatp3", Detail: adatp3URL, OK: &ok})
		} else {
			a.stats[i+2].sent += len(msgs)
			a.mu.Unlock()
			ok := true
			a.log.add(logEntry{Time: now(), Agent: a.cfg.agentID, Event: "adatp3", Detail: fmt.Sprintf("%s msgs=%d", adatp3URL, len(msgs)), OK: &ok})
		}
	}

	a.log.add(logEntry{Time: now(), Agent: a.cfg.agentID, Event: "cycle_done", Detail: fmt.Sprintf("cycle=%d", cycle)})
}

// moveUnits advances each unit by a random bearing/distance step.
func (a *agent) moveUnits() {
	b := a.scen.bounds
	for _, u := range a.units {
		// Gradually change bearing ±30° per cycle
		u.bearing += (a.rng.Float64()*60 - 30)
		if u.bearing < 0 {
			u.bearing += 360
		}
		if u.bearing >= 360 {
			u.bearing -= 360
		}
		dist := (0.3 + a.rng.Float64()*0.7) * a.scen.maxSpeedM
		newLat, newLon := movePoint(u.lat, u.lon, u.bearing, dist)

		// Reflect bearing if out of bounds
		if newLat < b.minLat || newLat > b.maxLat || newLon < b.minLon || newLon > b.maxLon {
			u.bearing = math.Mod(u.bearing+180, 360)
			newLat, newLon = movePoint(u.lat, u.lon, u.bearing, dist*0.5)
		}
		u.lat = clampf(newLat, b.minLat, b.maxLat)
		u.lon = clampf(newLon, b.minLon, b.maxLon)

		// Slowly vary strength (±1 per cycle)
		if a.rng.Intn(3) == 0 {
			u.strength += a.rng.Intn(3) - 1
			u.strength = int(clampf(float64(u.strength), 5, 35))
		}
	}
}

// ── Feature API posting ───────────────────────────────────────────────────────

func (a *agent) postFeatures(ctx context.Context, nodeURL string) error {
	lID := layerID(a.cfg.agentID)
	for i, u := range a.units {
		gj := map[string]any{
			"type": "Feature",
			"geometry": map[string]any{
				"type":        "Point",
				"coordinates": []float64{u.lon, u.lat},
			},
			"properties": map[string]any{
				"sidc":        u.sidc,
				"designation": fmt.Sprintf("A%d %s", a.cfg.agentID, u.name),
			},
		}
		gjRaw, _ := json.Marshal(gj)
		feat := map[string]any{
			"id":      featureID(a.cfg.agentID, i),
			"layerId": lID,
			"kind":    5, // KindTacticalSymbol
			"geoJson": json.RawMessage(gjRaw),
		}
		if _, err := a.httpPost(ctx, nodeURL+"/v1/features", feat); err != nil {
			return fmt.Errorf("feature %d: %w", i, err)
		}
	}
	return nil
}

// ── ADATP-3 generation and posting ───────────────────────────────────────────

var monthAbbr = [13]string{"", "JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}

func formatDTG(t time.Time) string {
	return fmt.Sprintf("%02d%02d%02dZ%s%02d", t.Day(), t.Hour(), t.Minute(), monthAbbr[t.Month()], t.Year()%100)
}

func (a *agent) generateADATP3(cycle int) []string {
	t := time.Now().UTC()
	dtg := formatDTG(t)
	var msgs []string
	serial := cycle * 100

	// 3× OWNSITREP — one per unit
	for i, u := range a.units {
		serial++
		msgs = append(msgs, fmt.Sprintf(
			"MSGID/OWNSITREP/%03d/%s/\nFROM/%s/\nTO/HIGHER HQ/\nUNIT/%s/\nLOCATION/WGS84/%.6f/%.6f/\nSTATUS/%s/\nSTRENGTH/%d/\nEQUIP/%s/\nENDREC/",
			serial, dtg, u.name, u.name, u.lat, u.lon, u.status, u.strength, u.equip,
		))
		_ = i
	}

	// 2× SITREP
	for k := 0; k < 2; k++ {
		serial++
		u := a.units[k%3]
		msgs = append(msgs, fmt.Sprintf(
			"MSGID/SITREP/%03d/%s/\nFROM/%s/\nTO/HIGHER HQ/\nUNIT/%s/\nLOCATION/WGS84/%.6f/%.6f/\nSITUATION/PATROL ONGOING SECTOR %d/\nENDREC/",
			serial, dtg, u.name, u.name, u.lat, u.lon, cycle%9+1,
		))
	}

	// 2× SPOTREP (enemy contact)
	for k := 0; k < 2; k++ {
		serial++
		// Generate contact position near a random unit
		ref := a.units[a.rng.Intn(3)]
		cLat := ref.lat + (a.rng.Float64()*0.1 - 0.05)
		cLon := ref.lon + (a.rng.Float64()*0.1 - 0.05)
		msgs = append(msgs, fmt.Sprintf(
			"MSGID/SPOTREP/%03d/%s/\nFROM/%s/\nTO/HIGHER HQ/\nLOCATION/WGS84/%.6f/%.6f/\nENEMY/\nSIZE/SQUAD/\nACTIVITY/MOVING EAST/\nEQUIP/2XBMP/\nENDREC/",
			serial, dtg, a.units[0].name, cLat, cLon,
		))
	}

	// 1× LOGREP
	serial++
	u := a.units[0]
	ammoStates := []string{"FULL", "HIGH", "ADEQUATE", "LOW"}
	fuelStates := []string{"FULL", "HIGH", "ADEQUATE", "LOW", "CRITICAL"}
	msgs = append(msgs, fmt.Sprintf(
		"MSGID/LOGREP/%03d/%s/\nFROM/%s/\nTO/LOG HQ/\nUNIT/%s/\nAMMO/%s/\nFUEL/%s/\nPERSONNEL/%d/%d/0/0/\nENDREC/",
		serial, dtg, u.name, u.name,
		ammoStates[a.rng.Intn(len(ammoStates))],
		fuelStates[a.rng.Intn(len(fuelStates))],
		u.strength, u.strength,
	))

	// 1× SPOTREP (unknown contact) or ORBAT (every 5th cycle)
	if cycle%5 == 0 {
		serial++
		ref := a.units[1]
		msgs = append(msgs, fmt.Sprintf(
			"MSGID/ORBAT/%03d/%s/\nFROM/%s/\nSIDE/BLUE/\nHQ/%s/\nECHELON/BATTALION/\nSUBUNIT/1.Kp/unit/COMPANY/\nSUBUNIT/2.Kp/unit/COMPANY/\nSUBUNIT/3.Kp/unit/COMPANY/\nENDREC/",
			serial, dtg, ref.name, ref.name,
		))
	} else {
		serial++
		ref := a.units[2]
		uLat := ref.lat + (a.rng.Float64()*0.2 - 0.1)
		uLon := ref.lon + (a.rng.Float64()*0.2 - 0.1)
		msgs = append(msgs, fmt.Sprintf(
			"MSGID/SPOTREP/%03d/%s/\nFROM/%s/\nTO/HIGHER HQ/\nLOCATION/WGS84/%.6f/%.6f/\nUNKNOWN/\nSIZE/VEHICLE/\nACTIVITY/STATIONARY/\nENDREC/",
			serial, dtg, ref.name, uLat, uLon,
		))
	}

	return msgs
}

func (a *agent) postADATP3(ctx context.Context, adatp3URL string, msgs []string) error {
	envelope := map[string]any{"messages": msgs}
	_, err := a.httpPost(ctx, adatp3URL+"/adatp3/message", envelope)
	return err
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func (a *agent) httpPost(ctx context.Context, url string, body any) ([]byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	var out []byte
	buf2 := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf2)
		out = append(out, buf2[:n]...)
		if e != nil {
			break
		}
	}
	return out, nil
}

func (a *agent) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		out = append(out, buf[:n]...)
		if e != nil {
			break
		}
	}
	return out, nil
}

// ── Health-aware startup ──────────────────────────────────────────────────────

func waitHealthy(ctx context.Context, url, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	c := &http.Client{Timeout: 5 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := c.Get(url + path)
		if err == nil && resp.StatusCode < 300 {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		slog.Info("waiting for dependency", "url", url+path)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("timeout waiting for %s%s", url, path)
}

// ── Control REST API ──────────────────────────────────────────────────────────

func (a *agent) serveControl(addr string) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /sim/status", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		resp := map[string]any{
			"agent":    a.cfg.agentID,
			"scenario": a.scen.name,
			"running":  a.running,
			"cycle":    a.cycle,
			"lastErr":  a.lastErr,
			"stats": map[string]any{
				"peer0_orbital_sent":   a.stats[0].sent,
				"peer0_orbital_errors": a.stats[0].errors,
				"peer1_orbital_sent":   a.stats[1].sent,
				"peer1_orbital_errors": a.stats[1].errors,
				"peer0_adatp3_sent":    a.stats[2].sent,
				"peer0_adatp3_errors":  a.stats[2].errors,
				"peer1_adatp3_sent":    a.stats[3].sent,
				"peer1_adatp3_errors":  a.stats[3].errors,
			},
			"units": func() []map[string]any {
				var out []map[string]any
				for _, u := range a.units {
					out = append(out, map[string]any{
						"name": u.name, "lat": u.lat, "lon": u.lon,
						"bearing": u.bearing, "strength": u.strength, "status": u.status,
					})
				}
				return out
			}(),
		}
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("POST /sim/start", func(w http.ResponseWriter, r *http.Request) {
		a.start()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /sim/stop", func(w http.ResponseWriter, r *http.Request) {
		a.stop()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /sim/step", func(w http.ResponseWriter, r *http.Request) {
		go a.step()
		w.WriteHeader(http.StatusAccepted)
	})

	mux.HandleFunc("POST /sim/reset", func(w http.ResponseWriter, r *http.Request) {
		a.stop()
		// Delete all sim features from peer nodes
		ctx := r.Context()
		for i := range a.units {
			fID := featureID(a.cfg.agentID, i)
			for _, peerURL := range a.cfg.peerOrbitalURLs {
				req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, peerURL+"/v1/features/"+fID, nil)
				resp, err := a.http.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}
		}
		a.mu.Lock()
		a.cycle = 0
		a.lastErr = ""
		a.stats = [4]deliveryStat{}
		// Reset positions
		b := a.scen.bounds
		for i, u := range a.units {
			latOffset := float64(a.cfg.agentID-1) * 0.5
			lonOffset := float64(i) * 0.3
			u.lat = clampf(b.minLat+(b.maxLat-b.minLat)*0.3+latOffset+a.rng.Float64()*0.5, b.minLat, b.maxLat)
			u.lon = clampf(b.minLon+(b.maxLon-b.minLon)*0.3+lonOffset+a.rng.Float64()*0.5, b.minLon, b.maxLon)
			u.bearing = a.rng.Float64() * 360
		}
		a.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /sim/log", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.log.all())
	})

	slog.Info("control API listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("control API error", "err", err)
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg := parseConfig()

	slog.Info("sim-agent starting",
		"agent", cfg.agentID,
		"scenario", cfg.scenario,
		"own", cfg.ownOrbitalURL,
		"peers_orbital", cfg.peerOrbitalURLs,
		"peers_adatp3", cfg.peerADATP3URLs,
	)

	ctx := context.Background()

	// Wait for own node
	slog.Info("waiting for own node", "url", cfg.ownOrbitalURL)
	if err := waitHealthy(ctx, cfg.ownOrbitalURL, "/healthz", cfg.startupTimeout); err != nil {
		slog.Error("own node not healthy", "err", err)
		os.Exit(1)
	}

	// Wait for peer ADATP-3 adapters
	for _, u := range cfg.peerADATP3URLs {
		slog.Info("waiting for ADATP-3 adapter", "url", u)
		if err := waitHealthy(ctx, u, "/health", cfg.startupTimeout); err != nil {
			slog.Error("ADATP-3 adapter not healthy", "url", u, "err", err)
			os.Exit(1)
		}
	}

	a := newAgent(cfg)

	// Create layers on all nodes
	slog.Info("setting up layers on all nodes")
	a.setupLayers(ctx)

	// Start control API in background
	go a.serveControl(cfg.listen)

	// Autostart simulation loop
	if cfg.autostart {
		slog.Info("autostarting simulation loop")
		a.start()
	}

	// Block forever (loop and control API run as goroutines)
	select {}
}
