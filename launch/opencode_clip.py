"""10 s announcement: Hall Monitor now supports opencode.

  python3 launch/opencode_clip.py <snapshot dir>   -> launch/out/hallmonitor-opencode.mp4

Composited frame by frame from the app's own renders (snapshot mode, demo
data), eased motion, motion blur from four sub-frames per frame, cut to
launch/out/drive.wav from bar 3, so the drop lands on the reveal.
"""
import math
import os
import subprocess
import sys

import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
SNAP = sys.argv[1]
OUT = os.path.join(HERE, "out")
FRAMES = os.path.join(OUT, "oc_frames")
FONTS = os.path.join(ROOT, "macos", "Fonts")
os.makedirs(FRAMES, exist_ok=True)

W, H, FPS, DUR = 1920, 1080, 30, 10.0
BEAT = 60 / 128
DROP = 8 * BEAT  # 3.75 s: bar 5 of the track, when the clip starts at bar 3
SUB = 12        # sub-frames per frame, for smooth motion blur

WHITE = (245, 245, 247, 255)
MUTED = (161, 161, 170, 255)


def font(weight, size):
    return ImageFont.truetype(os.path.join(FONTS, f"IBMPlexSans-{weight}.otf"), size)


def mono(size):
    return ImageFont.truetype(os.path.join(FONTS, "IBMPlexMono-Medium.otf"), size)


# ---- easing ----
def clamp(x, a=0.0, b=1.0):
    return max(a, min(b, x))


def prog(t, t0, d):
    return clamp((t - t0) / d)


def out_expo(x):
    return 1 if x >= 1 else 1 - 2 ** (-10 * x)


def out_cubic(x):
    return 1 - (1 - x) ** 3


def spring(x, bounce=0.3):
    if x <= 0:
        return 0.0
    if x >= 1:
        return 1.0
    return 1 - math.exp(-6.5 * x * (1 - bounce)) * math.cos(2 * math.pi * 1.35 * x)


# ---- layers, made once ----
def text_layer(s, f, fill=WHITE, pad=20):
    box = f.getbbox(s)
    img = Image.new("RGBA", (box[2] - box[0] + 2 * pad, box[3] - box[1] + 2 * pad), (0, 0, 0, 0))
    ImageDraw.Draw(img).text((pad - box[0], pad - box[1]), s, font=f, fill=fill)
    return img


def rounded(im, radius, border=(255, 255, 255, 34)):
    im = im.convert("RGBA")
    mask = Image.new("L", im.size, 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, im.width - 1, im.height - 1], radius=radius, fill=255)
    out = Image.new("RGBA", im.size, (0, 0, 0, 0))
    out.paste(im, (0, 0), mask)
    ImageDraw.Draw(out).rounded_rectangle([0, 0, im.width - 1, im.height - 1], radius=radius, outline=border, width=2)
    return out


def window(shot, width):
    im = Image.open(shot).convert("RGB")
    im = im.resize((width, int(im.height * width / im.width)), Image.LANCZOS)
    bar = int(30 * width / 1280)
    win = Image.new("RGB", (width, im.height + bar), (24, 25, 30))
    d = ImageDraw.Draw(win)
    r = max(5, bar // 5)
    for i, c in enumerate([(255, 95, 87), (254, 188, 46), (40, 200, 64)]):
        cx = bar // 2 + 4 + i * int(r * 3.4)
        d.ellipse([cx - r, bar / 2 - r, cx + r, bar / 2 + r], fill=c)
    win.paste(im, (0, bar))
    return rounded(win, int(16 * width / 1280))


def opencode_tile(size):
    """opencode's hollow block on a dark stone tile, as the app draws it."""
    s = 4
    img = Image.new("RGBA", (size * s, size * s), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.rounded_rectangle([0, 0, size * s - 1, size * s - 1], radius=int(size * s * 0.225), fill=(30, 28, 26, 255))
    a = size * s * 0.30
    t = size * s * 0.075
    c = size * s / 2
    d.rectangle([c - a / 2 - t, c - a * 0.62 - t, c + a / 2 + t, c + a * 0.62 + t], fill=(236, 232, 226, 255))
    d.rectangle([c - a / 2 + t * 0.4, c - a * 0.62 + t * 0.4, c + a / 2 - t * 0.4, c + a * 0.62 - t * 0.4], fill=(30, 28, 26, 255))
    d.rectangle([c - a / 2 + t * 1.4, c - a * 0.1, c + a / 2 - t * 1.4, c + a * 0.62 - t * 1.4], fill=(120, 116, 110, 255))
    return img.resize((size, size), Image.LANCZOS)


def backdrop():
    img = Image.new("RGB", (W, H), (14, 15, 18))
    light = Image.new("L", (W, H), 0)
    ImageDraw.Draw(light).ellipse([W * 0.1, -H * 0.9, W * 0.9, H * 0.55], fill=26)
    light = light.filter(ImageFilter.GaussianBlur(H * 0.25))
    return Image.composite(Image.new("RGB", (W, H), (40, 42, 48)), img, light).convert("RGBA")


def glow(color, r):
    # Room around the ellipse so the blur fades to nothing before the edge.
    g = Image.new("L", (2 * r, 2 * r), 0)
    m = r * 0.45
    ImageDraw.Draw(g).ellipse([m, m, 2 * r - m, 2 * r - m], fill=255)
    g = g.filter(ImageFilter.GaussianBlur(r * 0.22))
    img = Image.new("RGBA", g.size, color + (0,))
    img.putalpha(g)
    return img


BG = backdrop()
HM = Image.open(os.path.join(ROOT, "docs", "icon-1024.png")).convert("RGBA")
ICONS = os.path.join(OUT, "icons")
CLAUDE = Image.open(os.path.join(ICONS, "claude-app.png")).convert("RGBA")
CODEX = Image.open(os.path.join(ICONS, "codex-app.png")).convert("RGBA")
OC = opencode_tile(360)
WIN = window(os.path.join(SNAP, "window_agents_opencode.png"), 1500)
GLOW = glow((40, 150, 110), 520)

L = {
    "claude": text_layer("Claude Code.", font("SemiBold", 120)),
    "codex": text_layer("Codex.", font("SemiBold", 120)),
    "now": text_layer("And now…", font("SemiBold", 120)),
    "plus": text_layer("+", font("Regular", 120), MUTED),
    "with": text_layer("Hall Monitor now speaks opencode.", font("SemiBold", 64)),
    "cap": text_layer("opencode sessions, live.", font("SemiBold", 56)),
    "sub": text_layer("Right next to Claude Code and Codex: on the board, in the app, in usage.", font("Regular", 28), MUTED),
    "name": text_layer("Hall Monitor 0.5", font("SemiBold", 84)),
    "cmd": text_layer("brew upgrade hallmonitor", mono(34), (210, 210, 216, 255)),
}


def put(frame, layer, cx, cy, scale=1.0, alpha=1.0):
    """Composite `layer` centred at (cx, cy), scaled and faded."""
    if alpha <= 0.003 or scale <= 0.01:
        return
    im = layer
    if abs(scale - 1) > 0.002:
        im = im.resize((max(1, int(im.width * scale)), max(1, int(im.height * scale))), Image.BILINEAR)
    if alpha < 0.999:
        a = im.getchannel("A").point(lambda v: int(v * alpha))
        im = im.copy()
        im.putalpha(a)
    frame.alpha_composite(im, (int(cx - im.width / 2), int(cy - im.height / 2)))


def word(frame, t, layer, t0, t1, icon=None):
    """A word slammed in on a beat: scale 1.12 -> 1, held until t1."""
    if not (t0 <= t < t1):
        return
    k = out_expo(prog(t, t0, 0.18))
    s = 1.12 - 0.12 * k + 0.02 * prog(t, t0, t1 - t0)
    if icon is not None:
        put(frame, icon.resize((150, 150), Image.LANCZOS), W / 2 - layer.width * s / 2 - 70, H / 2, s, k)
        put(frame, layer, W / 2 + 75, H / 2, s, k)
    else:
        put(frame, layer, W / 2, H / 2, s, k)


def frame_at(t):
    f = BG.copy()
    # 1. build-up: the agents it already knows, then "and now…"; the last
    #    beat before the drop is silent and dark.
    word(f, t, L["claude"], 0, 2 * BEAT, CLAUDE)
    word(f, t, L["codex"], 2 * BEAT, 4 * BEAT, CODEX)
    word(f, t, L["now"], 4 * BEAT, 7 * BEAT)

    # 2. the drop: Hall Monitor + opencode.
    if DROP <= t < DROP + 1.8:
        k = spring(prog(t, DROP, 0.9), 0.32)
        a = clamp((t - DROP) / 0.08) * (1 - prog(t, DROP + 1.62, 0.18))
        put(f, GLOW, W / 2, H / 2 - 40, 1.0, 0.55 * a)
        gap = 300 * out_expo(prog(t, DROP, 0.6))
        put(f, HM.resize((380, 380), Image.LANCZOS), W / 2 - 110 - gap * 0.5, H / 2 - 60, k, a)
        put(f, OC, W / 2 + 110 + gap * 0.5, H / 2 - 60, k, a)
        put(f, L["plus"], W / 2, H / 2 - 66, 1, a * out_expo(prog(t, DROP + 0.25, 0.3)))
        put(f, L["with"], W / 2, H / 2 + 230 - 18 * (1 - out_expo(prog(t, DROP + 0.35, 0.5))), 1,
            a * out_expo(prog(t, DROP + 0.35, 0.4)))

    # 3. the app, filtered to opencode, rising in.
    t3 = DROP + 1.8
    if t3 <= t < t3 + 2.6:
        k = spring(prog(t, t3, 1.0), 0.18)
        a = clamp((t - t3) / 0.1) * (1 - prog(t, t3 + 2.42, 0.18))
        put(f, GLOW, W / 2, H - 150, 1.3, 0.45 * a)
        put(f, L["cap"], W / 2, 86, 1, a * out_expo(prog(t, t3 + 0.15, 0.4)))
        put(f, L["sub"], W / 2, 148, 1, a * out_expo(prog(t, t3 + 0.3, 0.4)))
        y = 735 + 420 * (1 - k) - 30 * prog(t, t3, 2.6)
        put(f, WIN, W / 2, y, 0.86 + 0.04 * k, a)

    # 4. end card.
    t4 = t3 + 2.6
    if t >= t4:
        k = spring(prog(t, t4, 0.8), 0.3)
        a = clamp((t - t4) / 0.1)
        put(f, GLOW, W / 2, H / 2, 1.0, 0.4 * a)
        put(f, HM.resize((220, 220), Image.LANCZOS), W / 2, H / 2 - 150, 0.8 + 0.2 * k, a)
        put(f, L["name"], W / 2, H / 2 + 20, 1, a * out_expo(prog(t, t4 + 0.15, 0.4)))
        put(f, L["cmd"], W / 2, H / 2 + 120, 1, a * out_expo(prog(t, t4 + 0.35, 0.4)))
    return f.convert("RGB")


if os.environ.get("PREVIEW"):
    for t in [float(x) for x in os.environ["PREVIEW"].split(",")]:
        frame_at(t).save(os.path.join(OUT, f"oc_preview_{t:05.2f}.png"))
    raise SystemExit

n = int(DUR * FPS)
for i in range(n):
    acc = np.zeros((H, W, 3), np.float32)
    for s in range(SUB):
        # Shutter: the first half of the frame, like a 180° camera.
        acc += np.asarray(frame_at((i + s / SUB * 0.5) / FPS), np.float32)
    Image.fromarray((acc / SUB).round().astype(np.uint8)).save(os.path.join(FRAMES, f"{i:04d}.png"))
    if i % 30 == 0:
        print("frame", i, flush=True)

audio = os.path.join(OUT, "oc_audio.wav")
start = 2 * 4 * BEAT  # bar 3
subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-ss", f"{start}", "-t", f"{DUR}",
                "-i", os.path.join(OUT, "drive.wav"),
                "-af", f"afade=t=in:d=0.05,afade=t=out:st={DUR - 0.9}:d=0.9", audio], check=True)
out = os.path.join(OUT, "hallmonitor-opencode.mp4")
subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-framerate", str(FPS),
                "-i", os.path.join(FRAMES, "%04d.png"), "-i", audio,
                "-c:v", "libx264", "-preset", "slow", "-crf", "16", "-pix_fmt", "yuv420p", "-movflags", "+faststart",
                "-c:a", "aac", "-b:a", "256k", "-shortest", out], check=True)
print("wrote", out)
