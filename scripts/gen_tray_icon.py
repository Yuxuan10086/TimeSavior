from PIL import Image, ImageDraw
import os


def draw_icon(size):
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    s = size / 64.0
    bg = (26, 35, 50, 255)
    accent = (61, 139, 253, 255)
    fg = (232, 238, 246, 255)
    d.rounded_rectangle([4 * s, 4 * s, 60 * s, 60 * s], radius=max(2, int(14 * s)), fill=bg)
    cx, cy, rad = 32 * s, 34 * s, 18 * s
    sw = max(2, int(round(3.5 * s)))
    d.ellipse([cx - rad, cy - rad, cx + rad, cy + rad], outline=accent, width=sw)
    hw = max(2, int(round(3 * s)))
    d.line([(cx, cy), (cx, 22 * s)], fill=fg, width=hw)
    d.line([(cx, cy), (42 * s, cy)], fill=fg, width=hw)
    cr = max(1.5, 2.5 * s)
    d.ellipse([cx - cr, cy - cr, cx + cr, cy + cr], fill=accent)
    d.polygon([(32 * s, 10 * s), (36 * s, 16 * s), (28 * s, 16 * s)], fill=accent)
    return img.convert("RGBA")


here = os.path.dirname(os.path.abspath(__file__))
project = os.path.dirname(here)
root = os.path.join(project, "assets")
ui = os.path.join(project, "internal", "ui")
os.makedirs(root, exist_ok=True)

sizes = [16, 32, 48, 64]
imgs = [draw_icon(s) for s in sizes]
ico_path = os.path.join(ui, "tray-icon.ico")
imgs[-1].save(ico_path, format="ICO", sizes=[(s, s) for s in sizes])
draw_icon(32).save(os.path.join(root, "tray-icon.png"))
draw_icon(32).save(os.path.join(ui, "tray-icon.png"))
for im in imgs:
    im.save(os.path.join(root, f"tray-icon-{im.width}.png"))
print("ico_bytes", os.path.getsize(ico_path))
