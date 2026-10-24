package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// validClientID: Spotify client IDs are 32 hex digits; anything else never
// reaches a URL.
func validClientID(id string) bool { return clientIDRe.MatchString(id) }

var clientIDRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

func (s *spotify) connect() {
	s.mu.Lock()
	id := strings.TrimSpace(s.clientID)
	old := s.cb
	s.cb = nil
	s.mu.Unlock()
	if old != nil {
		old.stop()
	}
	if !validClientID(id) {
		s.setProblem("That doesn't look like a Spotify client ID (32 letters and digits).")
		return
	}
	verifier, state := randomURLSafe(64), randomURLSafe(16)
	q := url.Values{
		"client_id":             {id},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"state":                 {state},
		"code_challenge_method": {"S256"},
		"code_challenge":        {challenge(verifier)},
	}
	cb, err := listen("127.0.0.1:43821", state, 5*time.Minute, func(code string, err error) {
		s.finishConnect(code, err, verifier, id)
	})
	if err != nil {
		s.setProblem("Couldn't listen on 127.0.0.1:43821 for Spotify's reply: " + err.Error())
		return
	}
	s.mu.Lock()
	s.cb, s.connecting, s.problem = cb, true, ""
	s.mu.Unlock()
	s.sendSettings()
	s.conn.Open(s.accounts + "/authorize?" + q.Encode())
}

func (s *spotify) finishConnect(code string, err error, verifier, clientID string) {
	s.mu.Lock()
	s.cb = nil
	s.mu.Unlock()
	if err == nil {
		err = s.token(url.Values{
			"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI},
			"client_id": {clientID}, "code_verifier": {verifier},
		})
	}
	s.mu.Lock()
	s.connecting = false
	if err != nil {
		s.problem = err.Error()
	} else {
		s.problem = ""
	}
	s.mu.Unlock()
	s.sendSettings()
	if err == nil {
		s.startPolling()
		go s.refreshNow()
	}
}

func (s *spotify) disconnect() {
	s.mu.Lock()
	cb := s.cb
	s.cb, s.connecting = nil, false
	s.access, s.refresh, s.expires = "", "", time.Time{}
	s.np, s.pausedFor, s.devices = nil, nil, nil
	s.mu.Unlock()
	if cb != nil {
		cb.stop()
	}
	s.conn.Secret("refresh_token", nil)
	s.conn.Card(nil)
	s.sendSettings()
}

// token posts to the token endpoint and keeps what comes back.
func (s *spotify) token(form url.Values) error {
	resp, err := s.http.PostForm(s.accounts+"/api/token", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var t struct {
		Access  string  `json:"access_token"`
		Expires float64 `json:"expires_in"`
		Refresh string  `json:"refresh_token"`
		Error   string  `json:"error"`
		Desc    string  `json:"error_description"`
	}
	json.Unmarshal(data, &t)
	if resp.StatusCode != 200 || t.Access == "" {
		if t.Error == "invalid_grant" && form.Get("grant_type") == "refresh_token" {
			s.disconnect()
			return errors.New("Spotify signed Hall Monitor out. Connect again in Settings.")
		}
		why := t.Desc
		if why == "" {
			why = t.Error
		}
		if why == "" {
			why = "HTTP " + resp.Status
		}
		return errors.New("Spotify refused the sign-in: " + why)
	}
	s.mu.Lock()
	s.access = t.Access
	s.expires = time.Now().Add(time.Duration(t.Expires-60) * time.Second)
	// A refresh may or may not hand out a new refresh token.
	changed := t.Refresh != "" && t.Refresh != s.refresh
	if changed {
		s.refresh = t.Refresh
	}
	s.mu.Unlock()
	if changed {
		s.conn.Secret("refresh_token", &t.Refresh)
	}
	return nil
}

func (s *spotify) validToken() (string, error) {
	s.mu.Lock()
	if s.access != "" && time.Now().Before(s.expires) {
		defer s.mu.Unlock()
		return s.access, nil
	}
	r, id := s.refresh, strings.TrimSpace(s.clientID)
	s.mu.Unlock()
	if r == "" {
		return "", errors.New("Not connected to Spotify.")
	}
	if err := s.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {r}, "client_id": {id}}); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access, nil
}

// MARK: the sign-in redirect

// callback waits on 127.0.0.1 (never another interface) for the one
// redirect back from Spotify, checks its state, and stops. It gives up
// after a while.
type callback struct {
	srv  *http.Server
	once sync.Once
	done func(string, error)
}

func listen(addr, state string, timeout time.Duration, done func(code string, err error)) (*callback, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &callback{done: done}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != "GET" {
			reply(w, 404, "Not found")
			return
		}
		if q.Get("state") != state {
			reply(w, 400, "This sign-in link doesn't match the one Hall Monitor started. Try Connect again.")
			return
		}
		if code := q.Get("code"); code != "" {
			reply(w, 200, "Hall Monitor is connected to Spotify. You can close this tab.")
			go c.finish(code, nil)
			return
		}
		e := q.Get("error")
		if e == "" {
			e = "unknown error"
		}
		reply(w, 200, "Spotify didn't connect ("+e+"). You can close this tab.")
		msg := "Spotify sign-in failed: " + e
		if e == "access_denied" {
			msg = "You declined the Spotify sign-in."
		}
		go c.finish("", errors.New(msg))
	})
	c.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go c.srv.Serve(l)
	time.AfterFunc(timeout, func() { c.finish("", errors.New("Spotify sign-in timed out. Try Connect again.")) })
	return c, nil
}

func (c *callback) finish(code string, err error) {
	c.once.Do(func() {
		go c.srv.Shutdown(context.Background())
		c.done(code, err)
	})
}

// stop gives up without telling anyone.
func (c *callback) stop() {
	c.once.Do(func() { c.srv.Shutdown(context.Background()) })
}

func reply(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	io.WriteString(w, `<!doctype html><meta charset=utf-8><title>Hall Monitor</title><body style="font:15px -apple-system;margin:4em;text-align:center">`+html.EscapeString(text))
}

// MARK: PKCE

func randomURLSafe(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
