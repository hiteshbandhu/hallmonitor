"""Social preview + Product Hunt gallery images, from the app's own renders.

  python3 launch/press.py <snapshot dir>   -> launch/out/press/*.png
"""
import os
import sys

from PIL import Image, ImageDraw, ImageFilter, ImageFont

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
SNAP = sys.argv[1]
OUT = os.path.join(HERE, "out", "press")
FONTS = os.path.join(ROOT, "macos", "Fonts")
os.makedirs(OUT, exist_ok=True)

CANVAS = (14, 15, 18)
WHITE = (245, 245, 247)
MUTED = (161, 161, 170)
FAINT = (113, 113, 122)


def font(weight, size):
    return ImageFont.truetype(os.path.join(FONTS, f"IBMPlexSans-{weight}.otf"), size)


def backdrop(w, h, glow=None):
    """Graphite with a soft light from above and an optional colour glow."""
    img = Image.new("RGB", (w, h), CANVAS)
    light = Image.new("L", (w, h), 0)
    ImageDraw.Draw(light).ellipse([w * 0.1, -h * 0.9, w * 0.9, h * 0.55], fill=26)
    light = light.filter(ImageFilter.GaussianBlur(h * 0.25))
    img = Image.composite(Image.new("RGB", (w, h), (40, 42, 48)), img, light)
    if glow:
        (cx, cy, r, color, a) = glow
        g = Image.new("L", (w, h), 0)
        ImageDraw.Draw(g).ellipse([cx - r, cy - r, cx + r, cy + r], fill=a)
        g = g.filter(ImageFilter.GaussianBlur(r * 0.6))
        img = Image.composite(Image.new("RGB", (w, h), color), img, g)
    return img


def crop_alpha(path, threshold=250):
    im = Image.open(path).convert("RGBA")
    box = im.getchannel("A").point(lambda a: 255 if a >= threshold else 0).getbbox()
    return im.crop(box)


def rounded(im, radius, border=(255, 255, 255, 30)):
    im = im.convert("RGBA")
    mask = Image.new("L", im.size, 0)
    ImageDraw.Draw(mask).rounded_rectangle([0, 0, im.width - 1, im.height - 1], radius=radius, fill=255)
    out = Image.new("RGBA", im.size, (0, 0, 0, 0))
    out.paste(im, (0, 0), mask)
    ImageDraw.Draw(out).rounded_rectangle([0, 0, im.width - 1, im.height - 1], radius=radius, outline=border, width=2)
    return out


def window(shot, width):
    """A macOS window around a screenshot: title bar, traffic lights."""
    im = Image.open(shot).convert("RGB")
    s = width / im.width
    im = im.resize((width, int(im.height * s)), Image.LANCZOS)
    bar = int(30 * width / 1280)
    win = Image.new("RGB", (width, im.height + bar), (24, 25, 30))
    d = ImageDraw.Draw(win)
    r = max(5, bar // 5)
    for i, c in enumerate([(255, 95, 87), (254, 188, 46), (40, 200, 64)]):
        cx = bar // 2 + 4 + i * int(r * 3.4)
        d.ellipse([cx - r, bar / 2 - r, cx + r, bar / 2 + r], fill=c)
    win.paste(im, (0, bar))
    return rounded(win, int(14 * width / 1280))


def text(d, xy, s, f, fill, anchor="la"):
    d.text(xy, s, font=f, fill=fill, anchor=anchor)


icon = Image.open(os.path.join(ROOT, "docs", "icon-1024.png")).convert("RGBA")

# 1. Social preview, 1280 x 640 (GitHub's size; also fine for X and Product Hunt).
W, H = 1280, 640
img = backdrop(W, H, glow=(1000, 420, 380, (40, 120, 90), 70)).convert("RGBA")
win = window(os.path.join(SNAP, "window_agents.png"), 760)
img.alpha_composite(win, (560, 150))
card = crop_alpha(os.path.join(SNAP, "notch_ask_question.png"))
card = card.resize((430, int(card.height * 430 / card.width)), Image.LANCZOS)
img.alpha_composite(rounded(card, 22, border=(255, 255, 255, 40)), (500, 262))
d = ImageDraw.Draw(img)
img.alpha_composite(icon.resize((128, 128), Image.LANCZOS), (64, 120))
text(d, (72, 290), "Hall Monitor", font("SemiBold", 62), WHITE)
text(d, (74, 370), "A hall monitor for your", font("Regular", 28), MUTED)
text(d, (74, 406), "coding agents.", font("Regular", 28), MUTED)
text(d, (74, 520), "Claude Code · Codex", font("Medium", 19), FAINT)
text(d, (74, 548), "macOS & Linux · open source", font("Medium", 19), FAINT)
img.convert("RGB").save(os.path.join(OUT, "1-social-preview.png"))

# 2. Answer from the notch, 1270 x 760.
W, H = 1270, 760
img = backdrop(W, H, glow=(900, 360, 420, (120, 80, 20), 55)).convert("RGBA")
d = ImageDraw.Draw(img)
d.rectangle([0, 0, W, 6], fill=(0, 0, 0))  # the screen's top edge
q = crop_alpha(os.path.join(SNAP, "notch_ask_question.png"))
q = q.resize((560, int(q.height * 560 / q.width)), Image.LANCZOS)
p = crop_alpha(os.path.join(SNAP, "notch_ask_permission.png"))
p = p.resize((500, int(p.height * 500 / p.width)), Image.LANCZOS)
img.alpha_composite(q, (W - 70 - 560, 0))            # hangs from the top, like the notch
img.alpha_composite(rounded(p, 22, border=(255, 255, 255, 40)), (W - 70 - 500 - 120, q.height + 40))
x = 80
text(d, (x, 250), "Answer your", font("SemiBold", 50), WHITE)
text(d, (x, 312), "agents from", font("SemiBold", 50), WHITE)
text(d, (x, 374), "the notch.", font("SemiBold", 50), WHITE)
for k, line in enumerate(["Questions and permission prompts", "drop out of the notch. One click,", "and your agent carries on."]):
    text(d, (x, 466 + k * 32), line, font("Regular", 21), MUTED)
img.convert("RGB").save(os.path.join(OUT, "2-answer-from-the-notch.png"))

# 3. Every agent, every machine, 1270 x 760.
W, H = 1270, 760
img = backdrop(W, H, glow=(635, 700, 520, (30, 110, 95), 60)).convert("RGBA")
d = ImageDraw.Draw(img)
text(d, (W // 2, 70), "Every agent, every machine, live.", font("SemiBold", 44), WHITE, "ma")
text(d, (W // 2, 132), "Claude Code and Codex on your Mac and your servers, in one native app.",
     font("Regular", 21), MUTED, "ma")
win = window(os.path.join(SNAP, "window_agents.png"), 1090)
img.alpha_composite(win, ((W - win.width) // 2, 196))
img.convert("RGB").save(os.path.join(OUT, "3-every-agent.png"))
print(sorted(os.listdir(OUT)))
