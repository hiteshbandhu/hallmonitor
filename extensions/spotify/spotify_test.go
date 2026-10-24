package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hiteshbandhu/hallmonitor/extension"
)

// app stands in for Hall Monitor: it collects what the extension sends.
type app struct {
	mu   sync.Mutex
	msgs []map[string]any
}

// Write takes each message as it's sent (one JSON line per Encode).
func (a *app) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			a.msgs = append(a.msgs, m)
		}
	}
	return len(p), nil
}

func (a *app) last(typ string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.msgs) - 1; i >= 0; i-- {
		if a.msgs[i]["type"] == typ {
			return a.msgs[i]
		}
	}
	return nil
}

func (a *app) count(typ string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, m := range a.msgs {
		if m["type"] == typ {
			n++
		}
	}
	return n
}

// fake is Spotify: the player API and the token endpoint.
type fake struct {
	mu       sync.Mutex
	calls    []string
	state    string // the GET body; "" answers 204
	tokenErr string
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/api/token" {
		r.ParseForm()
		f.calls = append(f.calls, "token "+r.Form.Get("grant_type"))
		if f.tokenErr != "" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"`+f.tokenErr+`"}`)
			return
		}
		io.WriteString(w, `{"access_token":"A","expires_in":3600,"refresh_token":"R2"}`)
		return
	}
	call := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/player")
	if q := r.URL.Query(); len(q) > 0 {
		call += "?" + q.Encode()
	}
	f.calls = append(f.calls, call)
	if r.Method == "GET" && strings.TrimPrefix(r.URL.Path, "/player") == "" {
		if f.state == "" {
			w.WriteHeader(204)
			return
		}
		io.WriteString(w, f.state)
		return
	}
	w.WriteHeader(204)
}

func (f *fake) seen(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func setup(t *testing.T) (*spotify, *app, *fake) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	a := &app{}
	s := newSpotify(extension.New(strings.NewReader(""), a))
	s.accounts, s.api = srv.URL, srv.URL+"/player"
	s.poll, s.settle = time.Hour, 0
	return s, a, f
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

const playing = `{"is_playing":true,"progress_ms":83000,"shuffle_state":true,"repeat_state":"context",
 "device":{"id":"d1","name":"MacBook Pro","volume_percent":60,"supports_volume":true},
 "item":{"id":"t1","name":"Midnight City","duration_ms":242000,"artists":[{"name":"M83"},{"name":"X"}],
  "album":{"images":[{"url":"https://i.scdn.co/640","width":640},{"url":"https://i.scdn.co/64","width":64},{"url":"https://i.scdn.co/300","width":300}]}},
 "actions":{"disallows":{"seeking":false,"skipping_prev":true}}}`

func TestParsePlayback(t *testing.T) {
	now := time.Now()
	n := parsePlayback([]byte(playing), now)
	if n == nil || n.title != "Midnight City" || n.artist != "M83, X" || n.trackID != "t1" {
		t.Fatalf("%+v", n)
	}
	if n.artURL != "https://i.scdn.co/300" { // smallest that's at least 120 wide
		t.Fatalf("cover %q", n.artURL)
	}
	if !n.playing || n.progress != 83 || n.duration != 242 || !n.shuffle || n.repeat != "context" {
		t.Fatalf("%+v", n)
	}
	if n.volume == nil || *n.volume != 60 || n.deviceName != "MacBook Pro" {
		t.Fatalf("%+v", n)
	}
	if len(n.disallows) != 1 || n.disallows[0] != "skipping_prev" {
		t.Fatalf("%v", n.disallows)
	}
	if n.position(now.Add(2*time.Second)) != 85 {
		t.Fatal(n.position(now.Add(2 * time.Second)))
	}
	// A device that can't change volume shows none.
	n = parsePlayback([]byte(strings.Replace(playing, `"supports_volume":true`, `"supports_volume":false`, 1)), now)
	if n.volume != nil {
		t.Fatal("volume")
	}
	if parsePlayback([]byte(`{"is_playing":false}`), now) != nil {
		t.Fatal("no item is nothing playing")
	}
}

// The form asks for a client ID until it's valid, then offers Connect;
// once connected it offers Disconnect.
func TestSettingsForm(t *testing.T) {
	s, a, _ := setup(t)
	s.handle(extension.In{Type: "hello", Settings: map[string]any{"client_id": "nope"}})
	form := func() []any { return a.last("settings")["form"].([]any) }
	button := func() map[string]any {
		for _, b := range form() {
			if m := b.(map[string]any); m["kind"] == "button" {
				return m
			}
		}
		return nil
	}
	if b := button(); b["action"] != "connect" || b["disabled"] != true {
		t.Fatalf("%v", b)
	}
	s.input("client_id", strings.Repeat("ab", 16))
	if b := button(); b["disabled"] == true {
		t.Fatalf("%v", b)
	}
	if got := a.last("save")["settings"].(map[string]any)["client_id"]; got != strings.Repeat("ab", 16) {
		t.Fatal(got)
	}
	s.mu.Lock()
	s.refresh = "R"
	s.mu.Unlock()
	s.sendSettings()
	if b := button(); b["action"] != "disconnect" {
		t.Fatalf("%v", b)
	}
}

// Connect opens Spotify's page; the redirect back with the right state
// signs in and saves the refresh token, a wrong state is refused.
func TestSignIn(t *testing.T) {
	s, a, f := setup(t)
	s.handle(extension.In{Type: "hello", Settings: map[string]any{"client_id": strings.Repeat("ab", 16)}})
	s.action("connect", nil)
	open := a.last("open")
	if open == nil {
		t.Fatal("no open")
	}
	u, _ := url.Parse(open["url"].(string))
	q := u.Query()
	if q.Get("redirect_uri") != redirectURI || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != strings.Repeat("ab", 16) {
		t.Fatal(u)
	}
	defer s.stop()
	resp, err := http.Get("http://127.0.0.1:43821/callback?state=wrong&code=c")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal(resp.StatusCode)
	}
	resp, err = http.Get("http://127.0.0.1:43821/callback?state=" + q.Get("state") + "&code=c")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	eventually(t, "secret saved", func() bool { return a.last("secret") != nil })
	if a.last("secret")["value"] != "R2" || !f.seen("token authorization_code") {
		t.Fatalf("%v", a.last("secret"))
	}
	eventually(t, "connected form", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.connecting && s.refresh == "R2"
	})
}

// A refresh token Spotify no longer takes signs out, clears it and says so.
func TestSignedOut(t *testing.T) {
	s, a, f := setup(t)
	f.tokenErr = "invalid_grant"
	s.handle(extension.In{Type: "hello", Settings: map[string]any{"client_id": strings.Repeat("ab", 16)}, Secrets: map[string]string{"refresh_token": "old"}})
	if _, err := s.validToken(); err == nil || !strings.Contains(err.Error(), "signed Hall Monitor out") {
		t.Fatal(err)
	}
	if sec := a.last("secret"); sec == nil || sec["value"] != nil {
		t.Fatalf("%v", sec)
	}
	if c := a.last("card"); c == nil || c["card"] != nil {
		t.Fatalf("%v", c)
	}
}

func connected(t *testing.T) (*spotify, *app, *fake) {
	s, a, f := setup(t)
	f.state = playing
	s.handle(extension.In{Type: "hello", Settings: map[string]any{"client_id": strings.Repeat("ab", 16)}, Secrets: map[string]string{"refresh_token": "R"}})
	eventually(t, "player card", func() bool {
		c := a.last("card")
		return c != nil && c["card"] != nil
	})
	return s, a, f
}

func TestCardAndControls(t *testing.T) {
	s, a, f := connected(t)
	p := a.last("card")["card"].(map[string]any)["player"].(map[string]any)
	if p["title"] != "Midnight City" || p["playing"] != true || p["repeat"] != "context" || p["device"] != "MacBook Pro" {
		t.Fatalf("%v", p)
	}
	n := a.count("card")
	s.action("toggle_play", nil)
	eventually(t, "paused at once", func() bool {
		return a.count("card") > n && a.last("card")["card"].(map[string]any)["player"].(map[string]any)["playing"] == false
	})
	eventually(t, "pause call", func() bool { return f.seen("PUT /pause?device_id=d1") })
	s.action("repeat", nil)
	eventually(t, "repeat one", func() bool { return f.seen("PUT /repeat?device_id=d1&state=track") })
	s.action("volume", map[string]any{"percent": 140.0})
	eventually(t, "volume clamped", func() bool { return f.seen("PUT /volume?device_id=d1&volume_percent=100") })
	// 83s in: previous restarts the track.
	s.action("previous", nil)
	eventually(t, "restart", func() bool { return f.seen("PUT /seek?device_id=d1&position_ms=0") })
	// Hidden: the card goes, even while playing.
	s.input("show", false)
	eventually(t, "hidden", func() bool { return a.last("card")["card"] == nil })
	time.Sleep(50 * time.Millisecond) // the controls' read-backs land too
	if a.last("card")["card"] != nil {
		t.Fatal("shown again")
	}
}

// An agent waiting pauses the music; once none waits it resumes, but not
// if you've moved to another track meanwhile.
func TestPauseForAgents(t *testing.T) {
	s, _, f := connected(t)
	s.agentsChanged([]extension.Session{{Key: "a", Status: "waiting"}})
	eventually(t, "paused", func() bool { return f.seen("PUT /pause") })
	f.mu.Lock()
	f.state = strings.Replace(playing, `"is_playing":true`, `"is_playing":false`, 1)
	f.mu.Unlock()
	s.agentsChanged(nil)
	eventually(t, "resumed", func() bool { return f.seen("PUT /play") })

	// Moved on: no resume.
	s2, _, f2 := connected(t)
	s2.agentsChanged([]extension.Session{{Key: "a", Status: "waiting"}})
	eventually(t, "paused", func() bool { return f2.seen("PUT /pause") })
	f2.mu.Lock()
	f2.state = strings.Replace(strings.Replace(playing, `"is_playing":true`, `"is_playing":false`, 1), `"id":"t1"`, `"id":"t2"`, 1)
	f2.mu.Unlock()
	s2.agentsChanged(nil)
	time.Sleep(2300 * time.Millisecond)
	if f2.seen("PUT /play") {
		t.Fatal("resumed a different track")
	}
}

func TestPKCE(t *testing.T) {
	// RFC 7636, appendix B.
	if got := challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatal(got)
	}
	if len(randomURLSafe(64)) != 86 || validClientID("xyz") || !validClientID(strings.Repeat("aF", 16)) {
		t.Fatal()
	}
}
