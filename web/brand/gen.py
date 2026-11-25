import sys

INK_LIGHT, ACCENT_LIGHT = "#14120b", "#0f7082"
INK_DARK, ACCENT_DARK = "#edecec", "#2fa3b8"
TILE = "#14120b"


def halves(size, pad):
    u = (size - 2 * pad) / 42
    cx = cy = size / 2
    r, g, d, c = 14 * u, u, 2 * u, u
    rr = r - c
    left = f"M{cx-g-c:.2f} {cy-rr-d:.2f}A{rr:.2f} {rr:.2f} 0 0 0 {cx-g-c:.2f} {cy+rr-d:.2f}Z"
    right = f"M{cx+g+c:.2f} {cy-rr+d:.2f}A{rr:.2f} {rr:.2f} 0 0 1 {cx+g+c:.2f} {cy+rr+d:.2f}Z"
    return left, right, c, f"rotate(45 {cx:.2f} {cy:.2f})"


def paths(size, pad, ink, accent):
    left, right, c, rot = halves(size, pad)
    if ink.startswith("class:"):
        a, b = f'class="{ink[6:]}"', f'class="{accent[6:]}"'
    else:
        a, b = f'fill="{ink}" stroke="{ink}"', f'fill="{accent}" stroke="{accent}"'
    stroke = f'stroke-width="{2*c:.2f}" stroke-linejoin="round"'
    return f'<g transform="{rot}"><path d="{left}" {a} {stroke}/><path d="{right}" {b} {stroke}/></g>'


def mark(size, pad, ink, accent, tile=None, radius=0.225):
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {size} {size}" width="{size}" height="{size}">']
    if tile:
        out.append(f'<rect width="{size}" height="{size}" rx="{size*radius:.1f}" fill="{tile}"/>')
    out.append(paths(size, pad, ink, accent))
    out.append("</svg>")
    return "".join(out)


def inline(size, pad):
    return f'<svg viewBox="0 0 {size} {size}" aria-hidden="true" class="size-[18px]">{paths(size, pad, "class:mark-ink", "class:mark-accent")}</svg>'


d = sys.argv[1]
open(f"{d}/logo-light.svg", "w").write(mark(512, 40, INK_LIGHT, ACCENT_LIGHT))
open(f"{d}/logo-dark.svg", "w").write(mark(512, 40, INK_DARK, ACCENT_DARK))
open(f"{d}/logo-small-light.svg", "w").write(mark(512, 24, INK_LIGHT, ACCENT_LIGHT))
open(f"{d}/logo-small-dark.svg", "w").write(mark(512, 24, INK_DARK, ACCENT_DARK))
open(f"{d}/app-icon.svg", "w").write(mark(512, 104, INK_DARK, ACCENT_DARK, TILE))
open(f"{d}/avatar.svg", "w").write(mark(1024, 190, INK_DARK, ACCENT_DARK, TILE, 0))
open(f"{d}/inline.svg", "w").write(inline(24, 1))
