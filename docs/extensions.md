# Extensions

Extensions add things to the notch that aren't agents, such as what's playing on Spotify. You install them from **Settings → Extensions**, where each has a switch and its own setup. This page is for people writing one. For a setup guide, see the extension's own page, for example [Spotify](extensions/spotify.md).

## How an extension works

An extension is a separate program. Hall Monitor starts it when the app starts, and they talk in JSON, one message per line: the app writes to the extension's stdin, the extension writes to stdout, and anything on stderr goes to its log. When stdin closes, the extension should exit.

The extension never draws anything itself. It sends:

- **a card** of a kind the app knows how to draw (today: `player`), which shows beside the agents;
- **a settings form**, which shows under its name in Settings.

It gets back what you pressed or typed.

### The sandbox

Hall Monitor can answer Claude Code's permission prompts, so an extension must not be able to. Each extension runs under `sandbox-exec` with a profile built from its manifest. It can:

- read the system, and its own folder;
- write only its `data/` folder, which is also its `HOME` and `TMPDIR`;
- connect out on port 443 (HTTPS) and look up names (DNS);
- listen on 127.0.0.1, only on the ports its manifest lists;
- run nothing but its own program.

So it can't read your projects, Claude's or Codex's files, or Hall Monitor's question and answer files. It can't write anywhere else in your home folder, open other ports, or start a shell. It doesn't see prompts or answers. With the `agents` permission it gets each agent's status under a key that says nothing else about it.

Hosts in `permissions.network` are what the extension says it talks to, and they're shown before you install. The sandbox can't limit HTTPS to particular hosts, so it allows any host on port 443.

## Files

An extension is a `.tar.gz` holding one folder:

```text
hallmonitor-spotify-1.0.0/
  manifest.json
  bin/hallmonitor-spotify     a universal (arm64 + x86_64) program
```

Hall Monitor installs it into `~/Library/Application Support/HallMonitor/Extensions/<id>/`. Next to the files above it adds `data/`, which is kept across updates, and `log.txt`, the extension's stderr, which starts over past 1 MB.

### manifest.json

```json
{
  "protocol": 1,
  "id": "spotify",
  "name": "Spotify",
  "version": "1.0.0",
  "summary": "What’s playing in the notch, with controls.",
  "executable": "bin/hallmonitor-spotify",
  "docs": "https://github.com/hiteshbandhu/hallmonitor/blob/main/docs/extensions/spotify.md",
  "symbol": "music.note",
  "cards": ["player"],
  "permissions": {
    "network": ["accounts.spotify.com", "api.spotify.com", "i.scdn.co"],
    "listen": [43821],
    "agents": true,
    "open": true
  }
}
```

| Field | |
|---|---|
| `protocol` | `1` |
| `id` | lowercase letters, digits and `-`, up to 32 characters; also the folder name |
| `executable` | path to the program inside the folder |
| `symbol` | an SF Symbol shown in Settings |
| `cards` | the card kinds it may send |
| `permissions.network` | hosts it talks to (shown to you; see above) |
| `permissions.listen` | up to 4 ports above 1023 it may listen on, on 127.0.0.1 only |
| `permissions.agents` | whether it gets agents' status |
| `permissions.open` | whether it may open web pages, for example a sign-in page |

## Messages

### From the app

| `type` | Fields | When |
|---|---|---|
| `hello` | `protocol`, `app` (version), `settings`, `secrets` | first, once per start |
| `action` | `name`, `args` | you pressed a card control or a form button |
| `input` | `key`, `value` | you changed a form field (a string) or switch (a bool) |
| `agents` | `sessions`: `[{key, status}]` | the agents changed (needs `agents`); status is `busy`, `waiting`, `idle`, `error` or `blocked` |

### From the extension

| `type` | Fields | |
|---|---|---|
| `card` | `card`: a card, or `null` | show it beside the agents, or take it away |
| `asset` | `key`, `data` (base64 PNG or JPEG, under 2 MB) | an image a card refers to by `key`; send it before the card. The app keeps the last 8 |
| `settings` | `form`: blocks | replace its settings form |
| `save` | `settings`: `{key: value}` | merge into the settings the app keeps (64 KB at most; `null` removes a key). They come back in the next `hello` |
| `secret` | `key`, `value` (or `null`) | keep a secret in the Keychain, or delete it; it comes back in `secrets` |
| `open` | `url` | open an https page. Needs `open`, and only works within 15 seconds of you pressing something of the extension's |

The app ignores messages it doesn't know, and stops an extension whose line grows past 8 MB.

### The `player` card

```json
{"kind": "player", "player": {
  "title": "Midnight City", "artist": "M83", "art": "cover-1a2b",
  "playing": true, "progress": 83.0, "duration": 243.0, "at": 1761300000000,
  "shuffle": true, "repeat": "context", "volume": 64,
  "device": "MacBook Pro", "devices": [{"id": "d1", "name": "MacBook Pro", "type": "computer", "active": true}],
  "disallows": ["skipping_prev"]
}}
```

`progress` is in seconds as of `at`, in Unix milliseconds, and the app moves the bar on from there. Leave out `volume` when it can't be changed. `disallows` greys out controls; it uses Spotify's names (`pausing`, `resuming`, `seeking`, `skipping_next`, `skipping_prev`, `toggling_shuffle`, `toggling_repeat_context`, `toggling_repeat_track`, `transferring_playback`). The app tints the notch's equalizer with the cover's color.

The card sends these actions:

| Action | `args` |
|---|---|
| `toggle_play`, `next`, `previous`, `shuffle`, `repeat` | none |
| `seek` | `seconds` |
| `volume` | `percent` |
| `devices` | none (the card appeared: a good time to list devices) |
| `play_on` | `device` (an id from `devices`) |

When several extensions send a card, the first one installed wins.

### Form blocks

| `kind` | Fields |
|---|---|
| `status` | `text`, `tone` (`ok`, `warn`, `error`), `label` (shown as a labeled row) |
| `input` | `key`, `label`, `value`, `placeholder`, `secret` |
| `toggle` | `key`, `label`, `value` |
| `button` | `action`, `label`, `primary` |
| `note` | `text`, `mono` (selectable code) |

`disabled` greys out an input, switch or button. An input sends `input` a moment after you stop typing. Send the form again whenever anything in it changes.

## Writing one in Go

The `extension` package in this repository speaks the protocol:

```go
import "github.com/hiteshbandhu/hallmonitor/extension"

func main() {
	c := extension.Stdio()
	for {
		m, err := c.Recv()
		if err != nil {
			return // the app closed stdin
		}
		switch m.Type {
		case "hello":
			c.Settings([]extension.Block{{Kind: "note", Text: "Hello from my extension."}})
		case "action":
			// m.Name, m.Args
		}
	}
}
```

[`extensions/spotify`](../extensions/spotify) is a complete one, with tests that stand in for both the app and Spotify. Any language works, as long as it reads and writes lines of JSON.

## Trying it out

```bash
scripts/extension.sh myext 0.1.0
/Applications/HallMonitor.app/Contents/MacOS/HallMonitor --install-extension dist/hallmonitor-myext-0.1.0.tar.gz
```

Then restart Hall Monitor. You can also use **Install from File…** in Settings → Extensions.

## The marketplace

The marketplace is [`extensions/index.json`](../extensions/index.json) on `main`. Each entry repeats the manifest's `id`, `name`, `summary`, `version`, `symbol`, `docs` and `permissions`, and adds:

- `url`: the `.tar.gz` (an https link, from the repository's releases);
- `sha256`: the archive's checksum;
- `min_app`: the oldest Hall Monitor it runs on.

`scripts/extension.sh` prints the entry. Hall Monitor installs an archive only if:

- its checksum matches;
- its manifest has the same id, version and permissions as the listing;
- it holds only plain files and folders, no links.

To publish:

1. Run `scripts/extension.sh <id> <version>`.
2. Attach the archive to a release tagged `<id>-v<version>`.
3. Add the printed entry to `extensions/index.json` on `main`.
