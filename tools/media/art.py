#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = ["pillow==12.2.0", "numpy==2.4.4"]
# ///
"""Draws the README's art from the kit's scenario goldens.

Every screen is one of the examples' or the template's real goldens (640x172),
so the art follows the UI. It writes into docs/images/:

  social-preview.png   1280x640, for GitHub's Settings > Social preview
  social-preview.gif   the same, its device cycling the gallery: the README's header
  board-demo.gif       the screens on the board, in the owner's photo (docs/images/board.jpg)
  how-it-works.png     one source, two targets, three kinds of driver
  skill-loop.png       the skill's seven steps
  sources.json         the sha256 of every golden it used

internal/repocheck's TestReadmeArtMatchesGoldens fails when a golden no longer
matches sources.json: run this again, look at the images, and commit them.

Usage, from anywhere (macOS: it needs Avenir Next and Menlo from /System/Library/Fonts):

  uv run tools/media/art.py   # Pillow 12.2.0 and numpy 2.4.4, from the block above
  python3 tools/media/art.py  # or with those two installed
"""

import hashlib
import json
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageFont

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "docs/images"
PHOTO = OUT / "board.jpg"

# From here to the next rule: copied from herdr-pager's tools/media/compose.py
# (the same owner, Apache-2.0), so the two READMEs' art is one family. The
# pager's orange accent is the kit's blue below.

AVENIR = "/System/Library/Fonts/Avenir Next.ttc"
MENLO = "/System/Library/Fonts/Menlo.ttc"
AVENIR_BOLD, AVENIR_DEMI, AVENIR_MEDIUM = 0, 2, 5  # faces in the collection

BG = (22, 19, 17)
GLASS = (10, 9, 8)
TEXT = (237, 228, 219)
MUTED = (150, 138, 128)
NUB = (62, 56, 52)


def font(path, size, index=0):
    return ImageFont.truetype(path, size, index=index)


def text_width(draw, text, f):
    return draw.textlength(text, font=f)


def draw_buttons(draw, buttons, origin_x, edge_y, scale, pressed, label_font=None):
    """Draws BOOT and PWR on the device's top edge, each above its hint cell."""
    for name, (x, w) in buttons.items():
        on = pressed.startswith(name)
        cx = origin_x + (x + w / 2) * scale
        half = 24 * scale ** 0.5
        draw.rounded_rectangle((cx - half, edge_y - 11, cx + half, edge_y - 1), radius=4, fill=ACCENT if on else NUB)
        if label_font:
            label = name.upper()
            draw.text((cx - text_width(draw, label, label_font) / 2, edge_y - 30), label, font=label_font,
                      fill=ACCENT if on else MUTED)


def spaced(draw, xy, text, f, fill, tracking):
    """Draws text with extra space between its letters."""
    x, y = xy
    for ch in text:
        draw.text((x, y), ch, font=f, fill=fill)
        x += text_width(draw, ch, f) + tracking
    return x - tracking


def spaced_width(draw, text, f, tracking):
    return sum(text_width(draw, ch, f) for ch in text) + tracking * (len(text) - 1)


def arrow(draw, x0, x1, y, colour, width=4, head=16):
    """A horizontal arrow from x0 to x1."""
    sign = 1 if x1 > x0 else -1
    draw.line((x0, y, x1 - sign * head, y), fill=colour, width=width)
    draw.polygon([(x1, y), (x1 - sign * head, y - head * 0.6), (x1 - sign * head, y + head * 0.6)], fill=colour)


# The board's glass in docs/images/board.jpg (the owner's photo, 2026-10-25),
# found by its dark pixels: top left, top right, bottom right, bottom left.
BOARD_GLASS = [(123.3, 380.4), (1000.5, 61.4), (1081.0, 336.5), (265.1, 723.8)]
# The lit area inside the glass, as fractions of its length and height.
BOARD_INSET = (0.035, 0.10)


def homography(src, dst):
    """The 3x3 projective map taking the four points src to dst."""
    rows, rhs = [], []
    for (x, y), (u, v) in zip(src, dst):
        rows.append([x, y, 1, 0, 0, 0, -u * x, -u * y]); rhs.append(u)
        rows.append([0, 0, 0, x, y, 1, -v * x, -v * y]); rhs.append(v)
    h = np.linalg.solve(np.array(rows, float), np.array(rhs, float))
    return np.append(h, 1).reshape(3, 3)


def apply(h, x, y):
    u, v, w = h @ [x, y, 1]
    return u / w, v / w


# ---- The kit's own. ----

# The gallery's selected-row blue (examples/gallery list.png), and the same lifted
# for a dark background, as compose.py lifts the pager's orange.
BLUE = (16, 101, 197)          # #1065C5
ACCENT = (82, 156, 240)        # #529CF0
AVENIR_HEAVY = 8               # a face in the collection
CHIP = (30, 32, 38)
CARD = (30, 31, 36)
EDGE = (60, 62, 70)
SOFT = (200, 194, 188)
BUTTONS = {"pwr": [483, 76], "boot": [559, 81]}  # herdr-pager's reel: the hint cells under each button

G = ROOT / "examples"
GALLERY = [G / "gallery/tests/goldens" / f"{n}.png" for n in ("list", "tiles", "card", "confirm", "setup", "toast")]
MORE = [ROOT / "templates/app/tests/goldens/home.png", G / "inputs/tests/goldens/home.png",
        G / "inputs/tests/goldens/wifi-connected.png"]
TAGS = ["esp32-s3", "esp-idf", "lvgl", "c", "go", "emulator", "mcp", "claude-code"]


def screen(path):
    return Image.open(path).convert("RGB")


def glow(im, box, colour, strength, blur):
    mask = Image.new("L", im.size, 0)
    ImageDraw.Draw(mask).ellipse(box, fill=strength)
    mask = mask.filter(ImageFilter.GaussianBlur(blur))
    return Image.composite(Image.new("RGB", im.size, colour), im, mask)


def pill(d, x, y, text, f, h=38, outline=ACCENT, fill=CHIP):
    w = text_width(d, text, f) + 30
    d.rounded_rectangle((x, y, x + w, y + h), radius=h // 2, fill=fill, outline=outline, width=2)
    a, de = f.getmetrics()
    d.text((x + 15, y + (h - a - de) / 2), text, font=f, fill=TEXT)
    return w


def one_palette(frames, base, box):
    """compose.py's trick: one palette from every frame; the background dithered
    once, the device pasted over it undithered."""
    thumbs = [f.resize((f.width // 2, f.height // 2), Image.Resampling.NEAREST) for f in frames]
    montage = Image.new("RGB", (thumbs[0].width, thumbs[0].height * len(thumbs)))
    for k, t in enumerate(thumbs):
        montage.paste(t, (0, k * t.height))
    palette = montage.quantize(colors=256, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE)
    still = base.quantize(palette=palette, dither=Image.Dither.FLOYDSTEINBERG)
    out = []
    for f in frames:
        q = still.copy()
        q.paste(f.quantize(palette=palette, dither=Image.Dither.NONE).crop(box), box[:2])
        out.append(q)
    return out


# 1. The social preview, and the same with its device cycling the gallery.
class Preview:
    width, height, scale, bez = 1280, 640, 1.45, 22

    def __init__(self):
        self.sw, self.sh = round(640 * self.scale), round(172 * self.scale)
        dw, dh = self.sw + 2 * self.bez, self.sh + 2 * self.bez
        self.dx, self.dy = (self.width - dw) // 2, self.height - dh - 40
        self.box = (self.dx, self.dy - 12, self.dx + dw, self.dy + dh)
        im = Image.new("RGB", (self.width, self.height), BG)
        im = glow(im, (self.dx - 60, self.dy - 40, self.dx + dw + 60, self.dy + dh + 80), BLUE, 95, 70)
        d = ImageDraw.Draw(im)
        d.text((78, 44), "lcd349-kit", font=font(AVENIR, 96, AVENIR_BOLD), fill=TEXT)
        d.text((82, 166), "Build for a 3.49-inch touch bar. Test it before it ships.",
               font=font(AVENIR, 34, AVENIR_MEDIUM), fill=(205, 198, 192))
        chip = font(MENLO, 19)
        x = 82
        for tag in TAGS:
            x += pill(d, x, 232, tag, chip) + 10
        d.rounded_rectangle((self.dx, self.dy, self.dx + dw, self.dy + dh), radius=26, fill=GLASS, outline=EDGE,
                            width=2)
        self.base = im

    def frame(self, scr):
        im = self.base.copy()
        im.paste(scr.resize((self.sw, self.sh), Image.Resampling.LANCZOS), (self.dx + self.bez, self.dy + self.bez))
        draw_buttons(ImageDraw.Draw(im), BUTTONS, self.dx + self.bez, self.dy, self.scale, "")
        return im


def social():
    p = Preview()
    frames = [p.frame(screen(f)) for f in GALLERY]
    frames[0].save(OUT / "social-preview.png", optimize=True)
    q = one_palette(frames, p.base, p.box)
    q[0].save(OUT / "social-preview.gif", save_all=True, append_images=q[1:], duration=1600, loop=0, disposal=1)


# 2. The kit's screens on the board, in the owner's photo (compose.py's compose_board).
def board():
    base = Image.open(PHOTO).convert("RGB")
    W, H = base.size
    unit = homography([(0, 0), (1, 0), (1, 1), (0, 1)], BOARD_GLASS)
    iu, iv = BOARD_INSET
    lit = [apply(unit, u, v) for u, v in [(iu, iv), (1 - iu, iv), (1 - iu, 1 - iv), (iu, 1 - iv)]]
    to_screen = homography(lit, [(0, 0), (640, 0), (640, 172), (0, 172)])
    coeffs = tuple((to_screen / to_screen[2, 2]).flatten()[:8])
    ss = 4
    big = Image.new("L", (W * ss, H * ss), 0)
    ImageDraw.Draw(big).polygon([(x * ss, y * ss) for x, y in lit], fill=255)
    alpha = np.asarray(big.resize((W, H), Image.Resampling.LANCZOS), float)[..., None] / 255
    photo = np.asarray(base, float)
    frames = []
    for f in GALLERY + MORE:
        warped = screen(f).transform((W, H), Image.Transform.PERSPECTIVE, coeffs, Image.Resampling.BICUBIC)
        warped = np.asarray(warped.filter(ImageFilter.GaussianBlur(0.6)), float)
        lit_px = np.clip(warped * 0.86 + photo, 0, 255)
        frames.append(Image.fromarray((photo * (1 - alpha) + lit_px * alpha).astype("uint8")))
    thumbs = [f.resize((W // 2, H // 2), Image.Resampling.NEAREST) for f in frames]
    montage = Image.new("RGB", (W // 2, H // 2 * len(thumbs)))
    for k, t in enumerate(thumbs):
        montage.paste(t, (0, k * (H // 2)))
    palette = montage.quantize(colors=256, method=Image.Quantize.MEDIANCUT, dither=Image.Dither.NONE)
    still = base.quantize(palette=palette, dither=Image.Dither.FLOYDSTEINBERG)
    mask = Image.fromarray(((alpha[..., 0] > 0.02) * 255).astype("uint8"))
    out = []
    for f in frames:
        q = still.copy()
        q.paste(f.quantize(palette=palette, dither=Image.Dither.NONE), (0, 0), mask)
        out.append(q)
    out[0].save(OUT / "board-demo.gif", save_all=True, append_images=out[1:], duration=1600, loop=0, disposal=1)


# 3. How it works: one source, two targets, three kinds of driver.
def elbow(d, x0, y0, x1, y1, colour, width=4, head=16):
    """From (x0, y0) right to the middle, along to y1, then right to (x1, y1), with a head."""
    mx = (x0 + x1) / 2
    d.line((x0, y0, mx, y0), fill=colour, width=width)
    d.line((mx, y0, mx, y1), fill=colour, width=width)
    arrow(d, mx - width / 2, x1, y1, colour, width, head)


def device(im, d, x, y, scr, scale, pad=14):
    sw, sh = round(640 * scale), round(172 * scale)
    d.rounded_rectangle((x, y, x + sw + 2 * pad, y + sh + 2 * pad), radius=16, fill=GLASS, outline=EDGE, width=2)
    im.paste(scr.resize((sw, sh), Image.Resampling.LANCZOS), (x + pad, y + pad))
    draw_buttons(d, BUTTONS, x + pad, y, scale, "")
    return sw + 2 * pad, sh + 2 * pad


def window(im, d, x, y, scr, scale, pad=14):
    sw, sh = round(640 * scale), round(172 * scale)
    bar = 30
    d.rounded_rectangle((x, y, x + sw + 2 * pad, y + bar + sh + 2 * pad), radius=16, fill=CARD, outline=EDGE, width=2)
    for i in range(3):
        d.ellipse((x + 18 + i * 20, y + 11, x + 29 + i * 20, y + 22), fill=(110, 112, 120))
    d.text((x + 90, y + 6), "lcd349-view", font=font(MENLO, 17), fill=MUTED)
    im.paste(scr.resize((sw, sh), Image.Resampling.LANCZOS), (x + pad, y + bar + pad))
    return sw + 2 * pad, bar + sh + 2 * pad


def label(d, cx, y, name, line):
    title = font(AVENIR, 30, AVENIR_HEAVY)
    sub = font(AVENIR, 24, AVENIR_MEDIUM)
    tw = spaced_width(d, name, title, 4)
    spaced(d, (cx - tw / 2, y), name, title, TEXT, 4)
    d.text((cx - text_width(d, line, sub) / 2, y + 42), line, font=sub, fill=MUTED)


def diagram():
    width, height = 1760, 760
    im = Image.new("RGB", (width, height), BG)
    # Source card, left.
    sx, sw_, sh_ = 40, 470, 236
    sy = 250
    im = glow(im, (sx - 40, sy - 80, sx + sw_ + 40, sy + sh_ + 80), BLUE, 55, 55)
    # Targets, middle: the board above, the emulator below.
    tx = 640
    by, ey = 70, 400
    d = ImageDraw.Draw(im)
    d.rounded_rectangle((sx, sy, sx + sw_, sy + sh_), radius=16, fill=CARD, outline=EDGE, width=2)
    for i in range(3):
        d.ellipse((sx + 20 + i * 22, sy + 18, sx + 32 + i * 22, sy + 30), fill=(110, 112, 120))
    d.text((sx + 100, sy + 12), "main/app.c", font=font(MENLO, 19), fill=MUTED)
    code = [("lcd349_init", "(NULL);"), ("lcd349_ui_lock", "();"), ("lv_list_create", "(scr);"),
            ("lcd349_buttons_subscribe", "(cb, NULL);"), ("lcd349_ui_unlock", "();")]
    mono = font(MENLO, 20)
    for i, (fn, rest) in enumerate(code):
        y = sy + 52 + i * 34
        d.text((sx + 28, y), fn, font=mono, fill=ACCENT if fn.startswith("lcd349") else SOFT)
        d.text((sx + 28 + text_width(d, fn, mono), y), rest, font=mono, fill=SOFT)
    label(d, sx + sw_ / 2, sy + sh_ + 30, "ONE SOURCE", "C, LVGL 9 and the lcd349 SDK")

    bw, bh = device(im, d, tx, by + 16, screen(GALLERY[0]), 0.62)
    label(d, tx + bw / 2, by + 16 + bh + 22, "THE BOARD", "ESP32-S3 · idf.py flash")
    ew, eh = window(im, d, tx, ey, screen(GALLERY[1]), 0.62)
    label(d, tx + ew / 2, ey + eh + 22, "THE EMULATOR", "linux target · headless, or a window")

    chip = font(MENLO, 22)
    src_mid = sy + sh_ / 2
    elbow(d, sx + sw_ + 16, src_mid, tx - 16, by + 16 + bh / 2, ACCENT)
    elbow(d, sx + sw_ + 16, src_mid, tx - 16, ey + 30 + (eh - 30) / 2, ACCENT)
    mx = (sx + sw_ + tx) / 2
    for y, t in [(by + 16 + bh / 2 - 46, "esp32s3"), (ey + 30 + (eh - 30) / 2 + 12, "linux")]:
        w = text_width(d, t, chip) + 30
        pill(d, mx - w - 16, y, t, chip, h=34)

    # Drivers, right: the same controls for everyone.
    dx, dw, dh = 1330, 390, 118
    drivers = [("PEOPLE", "the window, the lcd349 CLI", "click · keys b and p"),
               ("SCRIPTS", "scenarios with goldens", "lcd349 scenario · CI"),
               ("AGENTS", "17 MCP tools + the skill", "lcd349 mcp · Claude Code")]
    emu_mid = ey + 30 + (eh - 30) / 2
    title = font(AVENIR, 28, AVENIR_HEAVY)
    sub = font(AVENIR, 25, AVENIR_MEDIUM)
    small = font(MENLO, 22)
    tops = [70, 250, 430]
    for (name, line, how), y in zip(drivers, tops):
        d.rounded_rectangle((dx, y, dx + dw, y + dh), radius=16, fill=CARD,
                            outline=ACCENT if name == "AGENTS" else EDGE, width=2)
        spaced(d, (dx + 24, y + 16), name, title, TEXT, 3)
        d.text((dx + 24, y + 54), line, font=sub, fill=SOFT)
        d.text((dx + 24, y + 86), how, font=small, fill=MUTED)
        # Arrow from the driver left to the emulator.
        x0, x1 = dx - 16, tx + ew + 16
        mid = (x0 + x1) / 2
        d.line((x0, y + dh / 2, mid, y + dh / 2), fill=SOFT, width=3)
        d.line((mid, y + dh / 2, mid, emu_mid), fill=SOFT, width=3)
    mid = (dx - 16 + tx + ew + 16) / 2
    arrow(d, mid + 1.5, tx + ew + 16, emu_mid, SOFT, 3, 14)
    gx0, gx1 = tx + ew + 16, dx - 16
    for k, t in enumerate(["shot · touch", "press · lint", "wait · scenario"]):
        f = font(MENLO, 22)
        d.text((gx0 + 14, emu_mid + 16 + k * 30), t, font=f, fill=MUTED)
    label(d, dx + dw / 2, 600, "DRIVEN ALIKE", "by a person, a script or an agent")
    im.save(OUT / "how-it-works.png", optimize=True)


# 4. The skill's workflow: seven steps in two rows, at a width GitHub shows near 1:1.5.
def loop():
    steps = [("Scaffold", ["new_project", "device_info"]),
             ("Sketch", ["80 × 10 grid", "wireframes first"]),
             ("Build, run", ["build", "run"]),
             ("Check", ["screenshot", "ui_tree", "ui_lint", "touch · press", "wait"]),
             ("Fix, repeat", ["every screen,", "every state"]),
             ("Scenario", ["run_scenario", "goldens + lint"]),
             ("Hand over", ["the hardware", "checklist"])]
    mcp = {0, 2, 3, 5}
    width, height = 1200, 640
    margin, gap, rgap = 30, 24, 54
    cw = (width - 2 * margin - 3 * gap) / 4
    ch = 252
    rows = [30, 30 + ch + rgap]
    im = Image.new("RGB", (width, height), BG)
    cx4 = margin + 3 * (cw + gap)
    im = glow(im, (cx4 - 40, rows[0] - 40, cx4 + cw + 40, rows[0] + ch + 40), BLUE, 60, 45)
    d = ImageDraw.Draw(im)
    num = font(AVENIR, 30, AVENIR_BOLD)
    title = font(AVENIR, 30, AVENIR_DEMI)
    tool = font(MENLO, 22)
    pos = []
    for i, (name, tools) in enumerate(steps):
        r, c = divmod(i, 4)
        x, y = margin + c * (cw + gap), rows[r]
        pos.append((x, y))
        d.rounded_rectangle((x, y, x + cw, y + ch), radius=16, fill=CARD, outline=ACCENT if i == 3 else EDGE, width=2)
        rad = 24
        d.ellipse((x + 20, y + 20, x + 20 + 2 * rad, y + 20 + 2 * rad), fill=BLUE if i in mcp else (70, 72, 80))
        t = str(i + 1)
        d.text((x + 20 + rad - text_width(d, t, num) / 2, y + 22), t, font=num, fill=TEXT)
        d.text((x + 86, y + 26), name, font=title, fill=TEXT)
        for k, t in enumerate(tools):
            d.text((x + 22, y + 88 + k * 30), t, font=tool, fill=ACCENT if i in mcp else MUTED)
        if c < 3 and i < len(steps) - 1:
            arrow(d, x + cw + 3, x + cw + gap - 3, y + 44, SOFT, 3, 9)
    # 4 wraps to 5: down from 4, left along the gap, down into 5.
    x4, y4 = pos[3]
    x5, y5 = pos[4]
    gy = rows[0] + ch + rgap / 2
    d.line((x4 + cw / 2, rows[0] + ch + 2, x4 + cw / 2, gy), fill=SOFT, width=3)
    d.line((x5 + cw / 2, gy, x4 + cw / 2, gy), fill=SOFT, width=3)
    d.line((x5 + cw / 2, gy, x5 + cw / 2, y5 - 12), fill=SOFT, width=3)
    d.polygon([(x5 + cw / 2, y5 - 2), (x5 + cw / 2 - 9, y5 - 14), (x5 + cw / 2 + 9, y5 - 14)], fill=SOFT)
    # 5 loops back to 3.
    d.text((x5 + 22, y5 + ch - 52), "↻ back to 3 until", font=font(MENLO, 20), fill=ACCENT)
    d.text((x5 + 22, y5 + ch - 28), "  lint is clean", font=font(MENLO, 20), fill=ACCENT)
    # The key, in the free cell.
    kx, ky = margin + 3 * (cw + gap), rows[1]
    key = font(AVENIR, 23, AVENIR_MEDIUM)
    d.ellipse((kx + 4, ky + 30, kx + 24, ky + 50), fill=BLUE)
    for k, line in enumerate(["uses the lcd349", "MCP tools listed", "on its card"]):
        d.text((kx + 38, ky + 24 + k * 32), line, font=key, fill=MUTED)
    d.ellipse((kx + 4, ky + 150, kx + 24, ky + 170), fill=(70, 72, 80))
    for k, line in enumerate(["you and the agent,", "no tools"]):
        d.text((kx + 38, ky + 144 + k * 32), line, font=key, fill=MUTED)
    im.save(OUT / "skill-loop.png", optimize=True)


# 5. The record CI checks: which goldens the art shows, as they were drawn.
def sources():
    sums = {p.relative_to(ROOT).as_posix(): hashlib.sha256(p.read_bytes()).hexdigest() for p in GALLERY + MORE}
    (OUT / "sources.json").write_text(json.dumps(dict(sorted(sums.items())), indent=2) + "\n")


if __name__ == "__main__":
    OUT.mkdir(exist_ok=True)
    social()
    board()
    diagram()
    loop()
    sources()
    for p in sorted(OUT.iterdir()):
        print(f"{p.name}: {p.stat().st_size // 1024} KB")
