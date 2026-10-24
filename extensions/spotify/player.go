package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hiteshbandhu/hallmonitor/extension"
)

type nowPlaying struct {
	trackID, title, artist string
	artURL                 string
	playing                bool
	deviceID, deviceName   string
	progress, duration     float64 // seconds
	at                     time.Time
	shuffle                bool
	repeat                 string // off, context (all) or track (one)
	volume                 *int   // nil when the device can't change it
	disallows              []string
}

// position is where the track is now: progress moves on between polls.
func (n *nowPlaying) position(now time.Time) float64 {
	p := n.progress
	if n.playing {
		p += now.Sub(n.at).Seconds()
	}
	return min(n.duration, p)
}

// MARK: Web API

// call makes one request to the player API; a 401 refreshes the token and
// tries once more.
func (s *spotify) call(method, path, device string, query url.Values, body []byte) ([]byte, int, error) {
	return s.callOnce(method, path, device, query, body, true)
}

func (s *spotify) callOnce(method, path, device string, query url.Values, body []byte, retry bool) ([]byte, int, error) {
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	if device != "" {
		q.Set("device_id", device)
	}
	u := s.api + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	token, err := s.validToken()
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == 401 && retry:
		s.mu.Lock()
		s.expires = time.Time{}
		s.mu.Unlock()
		return s.callOnce(method, path, device, query, body, false)
	case resp.StatusCode == 429:
		wait, err := strconv.Atoi(resp.Header.Get("Retry-After"))
		if err != nil {
			wait = 30
		}
		s.mu.Lock()
		s.pollPaused = time.Now().Add(time.Duration(wait) * time.Second)
		s.mu.Unlock()
		return nil, 429, errors.New("Spotify asked Hall Monitor to slow down.")
	case resp.StatusCode == 403 && method != "GET":
		return nil, 403, errors.New("Spotify didn't allow that: control needs Premium, and some actions are off during ads.")
	}
	return data, resp.StatusCode, nil
}

// playback is what's playing now; nil when nothing is (Spotify answers 204).
func (s *spotify) playback() (*nowPlaying, error) {
	data, status, err := s.call("GET", "", "", nil, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, nil
	}
	return parsePlayback(data, time.Now()), nil
}

func parsePlayback(data []byte, now time.Time) *nowPlaying {
	type image struct {
		URL   string `json:"url"`
		Width int    `json:"width"`
	}
	var st struct {
		IsPlaying bool    `json:"is_playing"`
		Progress  float64 `json:"progress_ms"`
		Shuffle   bool    `json:"shuffle_state"`
		Repeat    string  `json:"repeat_state"`
		Device    *struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Volume         *int   `json:"volume_percent"`
			SupportsVolume *bool  `json:"supports_volume"`
		} `json:"device"`
		Item *struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
			Duration float64 `json:"duration_ms"`
			Artists  []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Images []image `json:"images"`
			} `json:"album"`
		} `json:"item"`
		Actions struct {
			Disallows map[string]bool `json:"disallows"`
		} `json:"actions"`
	}
	if json.Unmarshal(data, &st) != nil || st.Item == nil {
		return nil
	}
	it := st.Item
	// The smallest cover that's still sharp at 60 pt on a Retina screen.
	imgs := append([]image(nil), it.Album.Images...)
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Width < imgs[j].Width })
	cover := ""
	for _, im := range imgs {
		if im.Width >= 120 {
			cover = im.URL
			break
		}
	}
	if cover == "" && len(imgs) > 0 {
		cover = imgs[len(imgs)-1].URL
	}
	var artists []string
	for _, a := range it.Artists {
		artists = append(artists, a.Name)
	}
	n := &nowPlaying{
		trackID: it.ID, title: it.Name, artist: strings.Join(artists, ", "), artURL: cover,
		playing: st.IsPlaying, progress: st.Progress / 1000, duration: it.Duration / 1000, at: now,
		shuffle: st.Shuffle, repeat: st.Repeat,
	}
	if n.trackID == "" {
		n.trackID = it.Name
	}
	if n.repeat == "" {
		n.repeat = "off"
	}
	if d := st.Device; d != nil {
		n.deviceID, n.deviceName = d.ID, d.Name
		if d.SupportsVolume == nil || *d.SupportsVolume {
			n.volume = d.Volume
		}
	}
	for k, v := range st.Actions.Disallows {
		if v {
			n.disallows = append(n.disallows, k)
		}
	}
	sort.Strings(n.disallows)
	return n
}

// loadArt hands the app the cover: only from Spotify's own image hosts,
// over HTTPS, and small.
func (s *spotify) loadArt(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !(strings.HasSuffix(u.Host, ".scdn.co") || strings.HasSuffix(u.Host, ".spotifycdn.com")) {
		return
	}
	resp, err := s.http.Get(raw)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if resp.StatusCode != 200 || len(data) > 2<<20 || len(data) == 0 {
		return
	}
	sum := sha256.Sum256([]byte(raw))
	key := "cover-" + hex.EncodeToString(sum[:8])
	if s.conn.Asset(key, data) != nil {
		return
	}
	s.mu.Lock()
	still := s.np != nil && s.np.artURL == raw
	if still {
		s.artURL, s.artKey = raw, key
	}
	s.mu.Unlock()
	if still {
		s.sendCard()
	}
}

// MARK: controls
//
// Each control updates the notch at once, then reads the player back to
// correct it.

func (s *spotify) togglePlay() {
	s.mu.Lock()
	np := s.np
	if np == nil {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	wasPlaying := np.playing
	np.progress, np.at, np.playing = np.position(now), now, !np.playing
	device := np.deviceID
	s.mu.Unlock()
	s.sendCard()
	if wasPlaying {
		s.control("PUT", "/pause", device, nil, nil)
	} else {
		s.control("PUT", "/play", device, nil, nil)
	}
}

func (s *spotify) next() {
	s.control("POST", "/next", s.device(), nil, nil)
}

// previous is like a CD player: back to the start, or to the previous track
// when already near it.
func (s *spotify) previous() {
	s.mu.Lock()
	np := s.np
	if np == nil {
		s.mu.Unlock()
		return
	}
	restart := np.position(time.Now()) > 3 && !contains(np.disallows, "seeking")
	device := np.deviceID
	s.mu.Unlock()
	if restart {
		s.seek(0)
	} else {
		s.control("POST", "/previous", device, nil, nil)
	}
}

func (s *spotify) seek(seconds float64) {
	s.mu.Lock()
	np := s.np
	if np == nil {
		s.mu.Unlock()
		return
	}
	np.progress, np.at = max(0, seconds), time.Now()
	device := np.deviceID
	s.mu.Unlock()
	s.sendCard()
	s.control("PUT", "/seek", device, url.Values{"position_ms": {strconv.Itoa(int(seconds * 1000))}}, nil)
}

func (s *spotify) toggleShuffle() {
	s.mu.Lock()
	np := s.np
	if np == nil {
		s.mu.Unlock()
		return
	}
	np.shuffle = !np.shuffle
	state, device := strconv.FormatBool(np.shuffle), np.deviceID
	s.mu.Unlock()
	s.sendCard()
	s.control("PUT", "/shuffle", device, url.Values{"state": {state}}, nil)
}

// cycleRepeat goes off → all → one → off, as in Spotify itself.
func (s *spotify) cycleRepeat() {
	s.mu.Lock()
	np := s.np
	if np == nil {
		s.mu.Unlock()
		return
	}
	next := map[string]string{"off": "context", "context": "track"}[np.repeat]
	if next == "" {
		next = "off"
	}
	np.repeat = next
	device := np.deviceID
	s.mu.Unlock()
	s.sendCard()
	s.control("PUT", "/repeat", device, url.Values{"state": {next}}, nil)
}

func (s *spotify) setVolume(percent int) {
	s.mu.Lock()
	np := s.np
	if np == nil || np.volume == nil {
		s.mu.Unlock()
		return
	}
	v := min(100, max(0, percent))
	np.volume = &v
	device := np.deviceID
	s.mu.Unlock()
	s.sendCard()
	s.control("PUT", "/volume", device, url.Values{"volume_percent": {strconv.Itoa(v)}}, nil)
}

// loadDevices lists where Spotify can play now (open apps, speakers, the
// web player).
func (s *spotify) loadDevices() {
	data, status, err := s.call("GET", "/devices", "", nil, nil)
	if err != nil || status != 200 {
		return
	}
	var list struct {
		Devices []struct {
			ID     *string `json:"id"`
			Name   string  `json:"name"`
			Type   string  `json:"type"`
			Active bool    `json:"is_active"`
		} `json:"devices"`
	}
	if json.Unmarshal(data, &list) != nil {
		return
	}
	var out []extension.Device
	for _, d := range list.Devices {
		if d.ID != nil {
			out = append(out, extension.Device{ID: *d.ID, Name: d.Name, Type: strings.ToLower(d.Type), Active: d.Active})
		}
	}
	s.mu.Lock()
	s.devices = out
	s.mu.Unlock()
	s.sendCard()
}

// playOn moves playback to another device and keeps it playing.
func (s *spotify) playOn(id string) {
	s.mu.Lock()
	for i := range s.devices {
		s.devices[i].Active = s.devices[i].ID == id
		if s.devices[i].Active && s.np != nil {
			s.np.deviceName = s.devices[i].Name
		}
	}
	s.mu.Unlock()
	s.sendCard()
	body, _ := json.Marshal(map[string]any{"device_ids": []string{id}, "play": true})
	s.control("PUT", "", "", nil, body)
}

func (s *spotify) device() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.np == nil {
		return ""
	}
	return s.np.deviceID
}

func (s *spotify) control(method, path, device string, query url.Values, body []byte) {
	go func() {
		if _, _, err := s.call(method, path, device, query, body); err != nil {
			s.setProblem(err.Error())
		} else {
			s.setProblem("")
		}
		time.Sleep(s.settle)
		s.refreshNow()
	}()
}

// MARK: pause while an agent needs you

func (s *spotify) agentsChanged(sessions []extension.Session) {
	// Waiting for your answer, here or on another machine; an agent that
	// errored out doesn't hold your music hostage.
	now := map[string]bool{}
	for _, x := range sessions {
		if x.Status == "waiting" {
			now[x.Key] = true
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	arrived := false
	for k := range now {
		if !s.waiting[k] {
			arrived = true
		}
	}
	s.waiting = now
	if s.refresh == "" || !s.pause {
		return
	}
	if arrived {
		if s.resumeTimer != nil {
			s.resumeTimer.Stop()
			s.resumeTimer = nil
		}
		if s.pausedFor == nil {
			s.pausedFor = &paused{pending: true}
			go s.pauseForAgents()
		}
	} else if len(now) == 0 && s.pausedFor != nil && s.resumeTimer == nil {
		// A beat before resuming: agents often ask again right away.
		s.resumeTimer = time.AfterFunc(2*time.Second, func() {
			s.mu.Lock()
			s.resumeTimer = nil
			idle := len(s.waiting) == 0
			s.mu.Unlock()
			if idle {
				s.resumeAfterAgents()
			}
		})
	}
}

func (s *spotify) pauseForAgents() {
	np, err := s.playback()
	if err != nil || np == nil || !np.playing {
		s.mu.Lock()
		s.pausedFor = nil
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.pausedFor = &paused{device: np.deviceID, track: np.trackID}
	s.mu.Unlock()
	if _, _, err := s.call("PUT", "/pause", np.deviceID, nil, nil); err != nil {
		s.mu.Lock()
		s.pausedFor = nil
		s.mu.Unlock()
		s.setProblem(err.Error())
		return
	}
	s.mu.Lock()
	if s.np != nil {
		s.np.progress, s.np.at, s.np.playing = s.np.position(time.Now()), time.Now(), false
	}
	s.mu.Unlock()
	s.sendCard()
}

func (s *spotify) resumeAfterAgents() {
	s.mu.Lock()
	p := s.pausedFor
	s.pausedFor = nil
	s.mu.Unlock()
	if p == nil || p.pending {
		return
	}
	// Only if it's still the way we left it: you may have moved on.
	np, err := s.playback()
	if err != nil || np == nil || np.playing || np.trackID != p.track {
		return
	}
	s.call("PUT", "/play", p.device, nil, nil)
	s.refreshNow()
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
