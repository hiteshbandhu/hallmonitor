// Command hallmonitor-spotify is Hall Monitor's Spotify extension: it shows
// what's playing in the notch, with controls, and pauses it while an agent
// waits for you.
//
// It talks to Spotify's Web API (playback control needs Premium) with your
// own developer app's client ID, signed in with PKCE, so there is no client
// secret. The app keeps the refresh token in the Keychain for it; the access
// token lives only in memory here.
package main

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hiteshbandhu/hallmonitor/extension"
)

func main() {
	s := newSpotify(extension.Stdio())
	for {
		m, err := s.conn.Recv()
		if err != nil {
			s.stop()
			return
		}
		s.handle(m)
	}
}

const (
	port        = 43821
	redirectURI = "http://127.0.0.1:43821/callback"
	scopes      = "user-read-playback-state user-modify-playback-state"
)

type spotify struct {
	conn     *extension.Conn
	http     *http.Client
	accounts string // https://accounts.spotify.com, or a test server
	api      string // https://api.spotify.com/v1/me/player
	poll     time.Duration
	settle   time.Duration // after a control, before reading the state back

	// sendMu keeps cards in the order they were built, so a stale one can't
	// land after a newer one.
	sendMu      sync.Mutex
	mu          sync.Mutex
	clientID    string
	show, pause bool
	refresh     string // the refresh token; empty when signed out
	access      string
	expires     time.Time
	connecting  bool
	problem     string
	np          *nowPlaying
	devices     []extension.Device
	pollPaused  time.Time // after a 429
	cb          *callback
	artURL      string // the cover the app has, and its asset key
	artKey      string
	waiting     map[string]bool
	pausedFor   *paused // what Hall Monitor paused, until it resumes it
	resumeTimer *time.Timer
	polling     bool
}

type paused struct {
	pending       bool // reading the player before pausing it
	device, track string
}

func newSpotify(c *extension.Conn) *spotify {
	return &spotify{
		conn:     c,
		http:     &http.Client{Timeout: 8 * time.Second},
		accounts: "https://accounts.spotify.com",
		api:      "https://api.spotify.com/v1/me/player",
		poll:     5 * time.Second,
		settle:   600 * time.Millisecond,
		show:     true,
		pause:    true,
		waiting:  map[string]bool{},
	}
}

func (s *spotify) handle(m extension.In) {
	switch m.Type {
	case "hello":
		s.mu.Lock()
		if v, ok := m.Settings["client_id"].(string); ok {
			s.clientID = v
		}
		if v, ok := m.Settings["show"].(bool); ok {
			s.show = v
		}
		if v, ok := m.Settings["pause"].(bool); ok {
			s.pause = v
		}
		s.refresh = m.Secrets["refresh_token"]
		s.mu.Unlock()
		s.sendSettings()
		s.startPolling()
	case "input":
		s.input(m.Key, m.Value)
	case "action":
		s.action(m.Name, m.Args)
	case "agents":
		s.agentsChanged(m.Sessions)
	}
}

func (s *spotify) input(key string, value any) {
	s.mu.Lock()
	switch key {
	case "client_id":
		v, _ := value.(string)
		s.clientID = v
	case "show":
		s.show, _ = value.(bool)
	case "pause":
		s.pause, _ = value.(bool)
	default:
		s.mu.Unlock()
		return
	}
	save := map[string]any{"client_id": s.clientID, "show": s.show, "pause": s.pause}
	s.mu.Unlock()
	s.conn.Save(save)
	s.sendSettings()
	s.sendCard()
}

func (s *spotify) action(name string, args map[string]any) {
	switch name {
	case "connect":
		s.connect()
	case "disconnect":
		s.disconnect()
	case "toggle_play":
		s.togglePlay()
	case "next":
		s.next()
	case "previous":
		s.previous()
	case "seek":
		if v, ok := args["seconds"].(float64); ok {
			s.seek(v)
		}
	case "shuffle":
		s.toggleShuffle()
	case "repeat":
		s.cycleRepeat()
	case "volume":
		if v, ok := args["percent"].(float64); ok {
			s.setVolume(int(v))
		}
	case "devices":
		go s.loadDevices()
	case "play_on":
		if v, ok := args["device"].(string); ok {
			s.playOn(v)
		}
	}
}

func (s *spotify) stop() {
	s.mu.Lock()
	cb := s.cb
	s.cb = nil
	s.mu.Unlock()
	if cb != nil {
		cb.stop()
	}
}

// MARK: what the app shows

// sendSettings sends the form for Hall Monitor's settings, worded as it
// was when Spotify was built in.
func (s *spotify) sendSettings() {
	s.mu.Lock()
	connected := s.refresh != ""
	var form []extension.Block
	if connected {
		form = append(form,
			extension.Block{Kind: "status", Label: "Account", Text: "Connected"},
			extension.Block{Kind: "button", Action: "disconnect", Label: "Disconnect"})
	} else {
		label := "Connect"
		if s.connecting {
			label = "Waiting for your browser…"
		}
		form = append(form,
			extension.Block{Kind: "input", Key: "client_id", Label: "Client ID", Value: s.clientID, Placeholder: "32 letters and digits"},
			extension.Block{Kind: "button", Action: "connect", Label: label, Primary: true,
				Disabled: s.connecting || !validClientID(strings.TrimSpace(s.clientID))},
			extension.Block{Kind: "note", Text: "Needs Spotify Premium. Create an app at developer.spotify.com/dashboard with the Web API, add this redirect URI, and paste its client ID above:"},
			extension.Block{Kind: "note", Text: redirectURI, Mono: true})
	}
	if s.problem != "" {
		form = append(form, extension.Block{Kind: "status", Text: s.problem, Tone: "error"})
	}
	form = append(form,
		extension.Block{Kind: "toggle", Key: "show", Label: "Show what’s playing in the notch", Value: s.show, Disabled: !connected},
		extension.Block{Kind: "toggle", Key: "pause", Label: "Pause music while an agent waits", Value: s.pause, Disabled: !connected},
		extension.Block{Kind: "note", Text: "Music resumes once no agent is waiting, unless you changed it meanwhile. Your sign-in is kept in the Keychain."})
	s.mu.Unlock()
	s.conn.Settings(form)
}

// sendCard shows the player, or takes it away when nothing plays or you'd
// rather not see it.
func (s *spotify) sendCard() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	s.mu.Lock()
	np := s.np
	if np == nil || !s.show || s.refresh == "" {
		s.mu.Unlock()
		s.conn.Card(nil)
		return
	}
	p := &extension.Player{
		Title: np.title, Artist: np.artist, Playing: np.playing,
		Progress: np.progress, Duration: np.duration, At: np.at.UnixMilli(),
		Shuffle: &np.shuffle, Repeat: np.repeat, Volume: np.volume,
		Device: np.deviceName, Devices: s.devices, Disallows: np.disallows,
	}
	if np.artURL != "" && np.artURL == s.artURL {
		p.Art = s.artKey
	}
	s.mu.Unlock()
	s.conn.Card(&extension.Card{Kind: "player", Player: p})
}

func (s *spotify) setProblem(p string) {
	s.mu.Lock()
	changed := s.problem != p
	s.problem = p
	s.mu.Unlock()
	if changed {
		s.sendSettings()
	}
}

// MARK: polling

func (s *spotify) startPolling() {
	s.mu.Lock()
	if s.polling {
		s.mu.Unlock()
		return
	}
	s.polling = true
	s.mu.Unlock()
	go func() {
		s.refreshNow()
		t := time.NewTicker(s.poll)
		for range t.C {
			// Only while something uses it.
			s.mu.Lock()
			wanted := s.show || s.pause
			s.mu.Unlock()
			if wanted {
				s.refreshNow()
			}
		}
	}()
}

// refreshNow reads the player and shows it. A hiccup keeps the last state:
// it shouldn't blank the notch.
func (s *spotify) refreshNow() {
	s.mu.Lock()
	ok := s.refresh != "" && !time.Now().Before(s.pollPaused)
	s.mu.Unlock()
	if !ok {
		return
	}
	np, err := s.playback()
	if err != nil {
		return
	}
	s.mu.Lock()
	s.np = np
	needArt := np != nil && np.artURL != "" && np.artURL != s.artURL
	s.mu.Unlock()
	s.sendCard()
	if needArt {
		s.loadArt(np.artURL)
	}
}
