// Package icons finds a logo for each provider without shipping any brand
// artwork in hallmonitor itself. In order:
//
//  1. the icon of the vendor's app, if it's installed (Claude.app,
//     Codex.app, ChatGPT.app or OpenCode.app on macOS);
//  2. a copy cached from an earlier run;
//  3. for Claude, the Simple Icons mark from jsDelivr, pinned to one release,
//     drawn on a brand-colored tile and cached. Simple Icons dropped OpenAI's
//     mark in v16, so Codex instead gets a plain ">_" tile drawn locally, and
//     opencode a hollow block like its pixel logo;
//  4. nothing, and the board falls back to text marks.
package icons

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// SimpleIconsVersion is pinned so an upstream change can't alter or break
// the marks under us.
const SimpleIconsVersion = "16.33.0"

type spec struct {
	apps     []string // app bundles to borrow an icon from, in order
	slug     string   // simple-icons slug, "" for none
	svg      string   // local mark when there's no slug
	tile, fg color.RGBA
}

// promptSVG is a generic terminal prompt, ">_", in a 24×24 box.
const promptSVG = `<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg"><path d="M3.3 5.6 4.9 4l7.9 8-7.9 8-1.6-1.6L9.6 12z"/><path d="M13 17.6h8.5V20H13z"/></svg>`

// blockSVG is a hollow square, after opencode's pixel-block logo.
const blockSVG = `<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg"><path d="M5 3h14v4H5z"/><path d="M5 17h14v4H5z"/><path d="M5 3h4v18H5z"/><path d="M15 3h4v18h-4z"/></svg>`

var specs = map[string]spec{
	"claude": {
		apps: []string{"Claude.app"},
		slug: "claude",
		tile: color.RGBA{0xD9, 0x77, 0x57, 0xff}, fg: color.RGBA{0xff, 0xf8, 0xf2, 0xff},
	},
	"codex": {
		apps: []string{"Codex.app", "ChatGPT.app"},
		svg:  promptSVG,
		tile: color.RGBA{0x1f, 0x29, 0x37, 0xff}, fg: color.RGBA{0x7d, 0xd3, 0xfc, 0xff},
	},
	"opencode": {
		apps: []string{"OpenCode.app"},
		svg:  blockSVG,
		tile: color.RGBA{0x29, 0x25, 0x24, 0xff}, fg: color.RGBA{0xe7, 0xe5, 0xe4, 0xff},
	},
}

// Icon is a square logo plus where it came from ("app", "cache", "cdn").
type Icon struct {
	Image  image.Image
	Source string
}

// Load finds the icon for provider. allowNet gates the CDN fetch.
func Load(ctx context.Context, provider string, allowNet bool) (Icon, error) {
	sp, ok := specs[provider]
	if !ok {
		return Icon{}, fmt.Errorf("no icon for %q", provider)
	}
	if img, err := fromApps(sp.apps); err == nil {
		return Icon{img, "app"}, nil
	}
	if sp.slug == "" {
		img, err := tile([]byte(sp.svg), sp.tile, sp.fg, 256)
		if err != nil {
			return Icon{}, err
		}
		return Icon{img, "builtin"}, nil
	}
	cache := cachePath(provider)
	if img, err := readPNG(cache); err == nil {
		return Icon{img, "cache"}, nil
	}
	if !allowNet {
		return Icon{}, errors.New("no local icon and network fetch disabled")
	}
	img, err := fromCDN(ctx, sp)
	if err != nil {
		return Icon{}, err
	}
	_ = writePNG(cache, img)
	return Icon{img, "cdn"}, nil
}

func cachePath(provider string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "hallmonitor", "icons", fmt.Sprintf("%s-si%s.png", provider, SimpleIconsVersion))
}

// ---- installed apps ----

func fromApps(apps []string) (image.Image, error) {
	home, _ := os.UserHomeDir()
	for _, app := range apps {
		for _, root := range []string{"/Applications", filepath.Join(home, "Applications")} {
			bundle := filepath.Join(root, app)
			if _, err := os.Stat(bundle); err != nil {
				continue
			}
			for _, icns := range iconFiles(bundle) {
				if img, err := decodeICNS(icns); err == nil {
					return cropOpaque(img), nil
				}
			}
		}
	}
	return nil, errors.New("no app icon")
}

// iconFiles lists a bundle's .icns files, the one Info.plist names first.
func iconFiles(bundle string) []string {
	res := filepath.Join(bundle, "Contents", "Resources")
	var out []string
	if b, err := exec.Command("plutil", "-extract", "CFBundleIconFile", "raw", filepath.Join(bundle, "Contents", "Info.plist")).Output(); err == nil {
		name := strings.TrimSpace(string(b))
		if !strings.HasSuffix(name, ".icns") {
			name += ".icns"
		}
		out = append(out, filepath.Join(res, name))
	}
	all, _ := filepath.Glob(filepath.Join(res, "*.icns"))
	return append(out, all...)
}

// decodeICNS returns the best PNG-encoded image in an .icns file, preferring
// 256px, which is plenty for a terminal cell grid.
func decodeICNS(path string) (image.Image, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 8 || string(b[:4]) != "icns" {
		return nil, errors.New("not icns")
	}
	pref := map[string]int{"ic08": 0, "ic13": 1, "ic09": 2, "ic07": 3, "ic14": 4, "ic10": 5}
	best, bestRank := []byte(nil), 99
	for i := 8; i+8 <= len(b); {
		typ := string(b[i : i+4])
		n := int(binary.BigEndian.Uint32(b[i+4 : i+8]))
		if n < 8 || i+n > len(b) {
			break
		}
		data := b[i+8 : i+n]
		if r, ok := pref[typ]; ok && r < bestRank && bytes.HasPrefix(data, []byte("\x89PNG")) {
			best, bestRank = data, r
		}
		i += n
	}
	if best == nil {
		return nil, errors.New("no png in icns")
	}
	return png.Decode(bytes.NewReader(best))
}

// cropOpaque trims the transparent margin and drop shadow macOS icons carry,
// leaving the rounded tile.
func cropOpaque(img image.Image) image.Image {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0xf000 {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX <= minX || maxY <= minY {
		return img
	}
	// Square it around the center.
	side := max(maxX-minX, maxY-minY) + 1
	cx, cy := (minX+maxX)/2, (minY+maxY)/2
	r := image.Rect(cx-side/2, cy-side/2, cx-side/2+side, cy-side/2+side).Intersect(b)
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

// ---- CDN ----

func fromCDN(ctx context.Context, sp spec) (image.Image, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("https://cdn.jsdelivr.net/npm/simple-icons@%s/icons/%s.svg", SimpleIconsVersion, sp.slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("icon fetch: %s", resp.Status)
	}
	svg, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return nil, err
	}
	return tile(svg, sp.tile, sp.fg, 256)
}

// tile draws a single-color SVG mark centered on a rounded square.
func tile(svg []byte, bg, fg color.RGBA, size int) (image.Image, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	roundedRect(out, bg, size/5)

	mark := size * 62 / 100
	off := float64(size-mark) / 2
	icon.SetTarget(off, off, float64(mark), float64(mark))
	for i := range icon.SVGPaths {
		icon.SVGPaths[i].SetFillColor(fg)
	}
	sc := rasterx.NewScannerGV(size, size, out, out.Bounds())
	icon.Draw(rasterx.NewDasher(size, size, sc), 1)
	return out, nil
}

func roundedRect(img *image.RGBA, c color.RGBA, r int) {
	n := img.Bounds().Dx()
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := 0, 0
			if x < r {
				dx = r - x
			} else if x >= n-r {
				dx = x - (n - r - 1)
			}
			if y < r {
				dy = r - y
			} else if y >= n-r {
				dy = y - (n - r - 1)
			}
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// ---- cache ----

func readPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".icon-*")
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	f.Close()
	return os.Rename(f.Name(), path)
}
