import math, sys
C, S = math.cos(math.radians(30)), math.sin(math.radians(30))
def P(x, y, z): return ((x - y) * C, (x + y) * S - z)
def box(cx, cy, cz, hx, hy, hz):
    x0, x1, y0, y1, z0, z1 = cx-hx, cx+hx, cy-hy, cy+hy, cz-hz, cz+hz
    faces = {
        "left": [P(x0,y1,z1), P(x1,y1,z1), P(x1,y1,z0), P(x0,y1,z0)],
        "right": [P(x1,y0,z1), P(x1,y1,z1), P(x1,y1,z0), P(x1,y0,z0)],
        "top": [P(x0,y0,z1), P(x1,y0,z1), P(x1,y1,z1), P(x0,y1,z1)],
    }
    edge = [P(x1,y0,z1), P(x1,y1,z1), P(x0,y1,z1)]
    return cx+cy+cz, faces, edge
def build(A, B, W, D):
    items = [box(0,0,0,A,A,A)]
    for ax in range(3):
        c = [0,0,0]; c[ax] = D
        items.append(box(*c, B, B, B))
        m = [0,0,0]; m[ax] = (A + D - B) / 2
        h = [W, W, W]; h[ax] = (D - B - A) / 2 + 0.02
        items.append(box(*m, *h))
    items.sort(key=lambda t: t[0])
    return items
def svg(items, size, pad, sh, bg=None, edge=0.0):
    pts = [p for _, f, _ in items for face in f.values() for p in face]
    minx, maxx = min(p[0] for p in pts), max(p[0] for p in pts)
    miny, maxy = min(p[1] for p in pts), max(p[1] for p in pts)
    s = (size - 2*pad) / max(maxx-minx, maxy-miny)
    ox = (size - (maxx-minx)*s) / 2 - minx*s
    oy = (size - (maxy-miny)*s) / 2 - miny*s
    T = lambda p: f"{p[0]*s+ox:.2f} {p[1]*s+oy:.2f}"
    k = sh["key"]
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {size} {size}" width="{size}" height="{size}"><defs>']
    for name, (a, b) in sh["g"].items():
        out.append(f'<linearGradient id="{k}{name}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="{a}"/><stop offset="1" stop-color="{b}"/></linearGradient>')
    out.append("</defs>")
    if bg: out.append(f'<rect width="{size}" height="{size}" rx="{size*0.225:.1f}" fill="{bg}"/>')
    for _, f, e in items:
        for name in ("left", "right", "top"):
            out.append(f'<path d="M{"L".join(T(p) for p in f[name])}Z" fill="url(#{k}{name})"/>')
        if edge:
            out.append(f'<path d="M{"L".join(T(p) for p in e)}" fill="none" stroke="{sh["edge"]}" stroke-width="{edge*size:.2f}" stroke-linejoin="round" stroke-linecap="round"/>')
    out.append("</svg>")
    return "".join(out)
dark = {"key": "d", "edge": "rgba(255,255,255,0.9)", "g": {"top": ("#F7F6F3", "#DAD9D4"), "left": ("#B0AFA9", "#86857F"), "right": ("#64635E", "#42413D")}}
light = {"key": "l", "edge": "rgba(255,255,255,0.35)", "g": {"top": ("#ADACA6", "#8E8D87"), "left": ("#55544F", "#3A3935"), "right": ("#252422", "#121210")}}
large = build(1.0, 0.62, 0.25, 2.75)
small = build(1.0, 0.72, 0.36, 2.45)
d = sys.argv[1]
open(f"{d}/mark-dark.svg","w").write(svg(large, 512, 40, dark, edge=0.004))
open(f"{d}/mark-light.svg","w").write(svg(large, 512, 40, light, edge=0.003))
open(f"{d}/mark-small-dark.svg","w").write(svg(small, 512, 28, dark))
open(f"{d}/mark-small-light.svg","w").write(svg(small, 512, 28, light))
open(f"{d}/app-icon.svg","w").write(svg(small, 512, 104, dark, "#0E0E0D", edge=0.004))

def themed(items, size, pad):
    pts = [p for _, f, _ in items for face in f.values() for p in face]
    minx, maxx = min(p[0] for p in pts), max(p[0] for p in pts)
    miny, maxy = min(p[1] for p in pts), max(p[1] for p in pts)
    s = (size - 2*pad) / max(maxx-minx, maxy-miny)
    ox = (size - (maxx-minx)*s) / 2 - minx*s
    oy = (size - (maxy-miny)*s) / 2 - miny*s
    T = lambda p: f"{p[0]*s+ox:.2f} {p[1]*s+oy:.2f}"
    out = [f'<svg viewBox="0 0 {size} {size}" aria-hidden="true" class="size-[18px]"><defs>']
    for name in ("top", "left", "right"):
        out.append(f'<linearGradient id="bm-{name}" x1="0" y1="0" x2="0" y2="1"><stop offset="0" class="mark-{name}-1"/><stop offset="1" class="mark-{name}-2"/></linearGradient>')
    out.append("</defs>")
    for _, f, e in items:
        for name in ("left", "right", "top"):
            out.append(f'<path d="M{"L".join(T(p) for p in f[name])}Z" fill="url(#bm-{name})"/>')
    out.append("</svg>")
    return "".join(out)
open(f"{d}/inline.svg", "w").write(themed(small, 24, 0.5))

def avatar(items, size, pad, sh, bg):
    out = svg(items, size, pad, sh, edge=0.004)
    return out.replace("</defs>", f'</defs><rect width="{size}" height="{size}" fill="{bg}"/>', 1)
open(f"{d}/avatar.svg", "w").write(avatar(small, 1024, 190, dark, "#0E0E0D"))
