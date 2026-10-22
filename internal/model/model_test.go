package model

import "testing"

func TestCleanDropsEscapes(t *testing.T) {
	for in, want := range map[string]string{
		"ok\x1b]2;EVIL\x07":              "ok]2;EVIL",
		"\x1b]52;c;aGVsbG8=\x07paste":    "]52;c;aGVsbG8=paste",
		"csi\u009b31mred":                "csi31mred",
		"raw\x9bbyte":                    "rawbyte",
		"bidi‮txt.exe":                   "biditxt.exe",
		"lines\nand\ttabs stay, \r goes": "lines\nand\ttabs stay,  goes",
		"plain · émoji 👍 and 漢字":         "plain · émoji 👍 and 漢字",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Snip("a\x1b[2Jb\r\nc", 20); got != "a[2Jb c" {
		t.Errorf("Snip = %q", got)
	}
}

func TestSanitize(t *testing.T) {
	s := Session{Title: "t\x1b]0;x\x07", Extra: map[string]string{"k": "\x07v"}, Subagents: &Subagents{Active: []string{"\x1bz"}}}
	s.Sanitize()
	if s.Title != "t]0;x" || s.Extra["k"] != "v" || s.Subagents.Active[0] != "z" {
		t.Fatalf("%+v %+v", s, s.Subagents)
	}
}
