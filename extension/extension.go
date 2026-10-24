// Package extension is the Go side of Hall Monitor's extension protocol.
//
// An extension is a separate program the app starts in a sandbox. They
// talk in JSON, one message per line: the app writes to the extension's
// stdin, the extension answers on stdout, and anything on stderr goes to
// its log. The app draws everything; an extension only sends what to show
// (a card of a kind the app knows, a settings form) and gets back what the
// user did. When stdin closes, the extension should exit.
//
// See docs/extensions.md for the whole protocol.
package extension

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"sync"
)

// Protocol is the version of the protocol this package speaks.
const Protocol = 1

// In is a message from the app. Type says which fields are set:
//
//	hello   Protocol, App, Settings, Secrets: sent once, first
//	action  Name, Args: the user pressed something on a card or form
//	input   Key, Value: the user changed a field or switch on the form
//	agents  Sessions: the agents changed (only with the agents permission)
type In struct {
	Type     string            `json:"type"`
	Protocol int               `json:"protocol,omitempty"`
	App      string            `json:"app,omitempty"`
	Settings map[string]any    `json:"settings,omitempty"`
	Secrets  map[string]string `json:"secrets,omitempty"`
	Name     string            `json:"name,omitempty"`
	Args     map[string]any    `json:"args,omitempty"`
	Key      string            `json:"key,omitempty"`
	Value    any               `json:"value,omitempty"`
	Sessions []Session         `json:"sessions,omitempty"`
}

// Session is one agent, as much as an extension gets to know: an opaque
// key and its status (busy, waiting, idle, error, blocked).
type Session struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

// Card is what the extension shows in the notch, beside the agents. Kind
// picks how the app draws it; only the matching field is read.
type Card struct {
	Kind   string  `json:"kind"`
	Player *Player `json:"player,omitempty"`
}

// Player is a "player" card: a track, its cover and the usual controls.
// The app sends these actions back: toggle_play, next, previous,
// seek {seconds}, shuffle, repeat, volume {percent}, devices (the device
// menu opened) and play_on {device}.
type Player struct {
	Title    string  `json:"title"`
	Artist   string  `json:"artist"`
	Art      string  `json:"art,omitempty"` // an asset key
	Playing  bool    `json:"playing"`
	Progress float64 `json:"progress"` // seconds, as of At
	Duration float64 `json:"duration"`
	At       int64   `json:"at"` // unix milliseconds when Progress was read
	// Shuffle and Repeat are nil where the player has none.
	Shuffle   *bool    `json:"shuffle,omitempty"`
	Repeat    string   `json:"repeat,omitempty"` // off, context (all) or track (one)
	Volume    *int     `json:"volume,omitempty"` // 0-100; nil when it can't change
	Device    string   `json:"device,omitempty"`
	Devices   []Device `json:"devices,omitempty"`
	Disallows []string `json:"disallows,omitempty"` // seeking, skipping_next, pausing…
}

// Device is somewhere the player can play.
type Device struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"` // computer, smartphone, speaker, tv, tablet, automobile
	Active bool   `json:"active"`
}

// Block is one row of the extension's settings form:
//
//	status  Text, Tone (ok, warn, error, or none)
//	input   Key, Label, Value (string), Placeholder; Secret hides it
//	toggle  Key, Label, Value (bool)
//	button  Action, Label; Primary
//	note    Text; Mono shows it as selectable code
//
// Disabled greys out an input, toggle or button.
type Block struct {
	Kind        string `json:"kind"`
	Key         string `json:"key,omitempty"`
	Label       string `json:"label,omitempty"`
	Text        string `json:"text,omitempty"`
	Tone        string `json:"tone,omitempty"`
	Value       any    `json:"value,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Action      string `json:"action,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Primary     bool   `json:"primary,omitempty"`
	Mono        bool   `json:"mono,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

// Conn is the line to the app. Its methods are safe to call from any
// goroutine.
type Conn struct {
	mu  sync.Mutex
	out *json.Encoder
	in  *bufio.Scanner
}

// Stdio is the connection every extension uses: stdin and stdout.
func Stdio() *Conn { return New(os.Stdin, os.Stdout) }

// New talks over r and w (tests use pipes).
func New(r io.Reader, w io.Writer) *Conn {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), 1<<20)
	return &Conn{out: json.NewEncoder(w), in: s}
}

// Recv waits for the next message; io.EOF means the app is done with us.
func (c *Conn) Recv() (In, error) {
	for c.in.Scan() {
		var m In
		if json.Unmarshal(c.in.Bytes(), &m) == nil && m.Type != "" {
			return m, nil
		}
	}
	if err := c.in.Err(); err != nil {
		return In{}, err
	}
	return In{}, io.EOF
}

func (c *Conn) send(m map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.out.Encode(m)
}

// Card shows card in the notch; nil takes it away.
func (c *Conn) Card(card *Card) error { return c.send(map[string]any{"type": "card", "card": card}) }

// Asset hands the app an image (PNG or JPEG, under 2 MB) that cards refer
// to by key. Send it before the card that uses it.
func (c *Conn) Asset(key string, data []byte) error {
	return c.send(map[string]any{"type": "asset", "key": key, "data": base64.StdEncoding.EncodeToString(data)})
}

// Settings replaces the extension's form in Hall Monitor's settings.
func (c *Conn) Settings(form []Block) error {
	if form == nil {
		form = []Block{}
	}
	return c.send(map[string]any{"type": "settings", "form": form})
}

// Save merges values into the settings the app keeps for the extension;
// they come back in the next hello. Plain JSON values only.
func (c *Conn) Save(values map[string]any) error {
	return c.send(map[string]any{"type": "save", "settings": values})
}

// Secret keeps value in the Keychain under key, or deletes it when value
// is nil. Secrets come back in the next hello.
func (c *Conn) Secret(key string, value *string) error {
	return c.send(map[string]any{"type": "secret", "key": key, "value": value})
}

// Open asks the app to open an https URL in the browser. The app only does
// so right after the user pressed something for this extension.
func (c *Conn) Open(url string) error { return c.send(map[string]any{"type": "open", "url": url}) }
