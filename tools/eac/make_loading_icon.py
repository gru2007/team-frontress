#!/usr/bin/env python3
"""Draw the Easy Anti-Cheat shield shown on the loading dialog.

Writes game/tc2/loose/materials/vgui/resource/icon_eac.vtf: 64x64 BGRA8888,
one mip, no Valve tools needed. Our own art (a shield with a check), standing
where VAC's icon stood in resource/loadingdialog*.res. Re-run after changing
the colours or the shape.
"""

import os
import struct

SIZE = 64
SS = 4  # supersampling per axis

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "..", "..", "game", "tc2", "loose", "materials", "vgui", "resource", "icon_eac.vtf")

TOP = (64, 156, 232)      # shield, top
BOTTOM = (22, 84, 156)    # shield, bottom
RIM = (205, 230, 255)
CHECK = (255, 255, 255)


def in_shield(x, y, inset=0.0):
    """Shield in unit space: flat top with rounded shoulders, sides that
    curve to a point at the bottom."""
    top, bottom = 0.08 + inset, 0.94 - inset * 1.6
    if y < top or y > bottom:
        return False
    t = (y - top) / (bottom - top)
    half = 0.40 - inset
    if t > 0.45:
        k = (t - 0.45) / 0.55
        half *= (1.0 - k * k) ** 0.75
    if t < 0.08:  # rounded shoulders
        half -= 0.06 * (1.0 - t / 0.08) ** 2
    return abs(x - 0.5) <= half


def dist_to_segment(px, py, ax, ay, bx, by):
    dx, dy = bx - ax, by - ay
    t = max(0.0, min(1.0, ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy)))
    cx, cy = ax + t * dx, ay + t * dy
    return ((px - cx) ** 2 + (py - cy) ** 2) ** 0.5


def in_check(x, y):
    w = 0.055
    return (dist_to_segment(x, y, 0.31, 0.50, 0.45, 0.65) <= w or
            dist_to_segment(x, y, 0.45, 0.65, 0.70, 0.34) <= w)


def shade(x, y):
    """(r, g, b, a) at a point in unit space, before supersampling."""
    if not in_shield(x, y):
        return (0, 0, 0, 0)
    if in_check(x, y):
        return CHECK + (255,)
    if not in_shield(x, y, inset=0.045):
        return RIM + (255,)
    t = min(max((y - 0.08) / 0.86, 0.0), 1.0)
    c = tuple(int(TOP[i] + (BOTTOM[i] - TOP[i]) * t) for i in range(3))
    return c + (255,)


def main():
    pixels = bytearray()
    n = SS * SS
    for py in range(SIZE):
        for px in range(SIZE):
            acc = [0, 0, 0, 0]
            for sy in range(SS):
                for sx in range(SS):
                    r, g, b, a = shade((px + (sx + 0.5) / SS) / SIZE, (py + (sy + 0.5) / SS) / SIZE)
                    acc[0] += r * a
                    acc[1] += g * a
                    acc[2] += b * a
                    acc[3] += a
            alpha = acc[3] // n
            if acc[3]:
                r, g, b = (acc[i] // acc[3] for i in range(3))
            else:
                r = g = b = 0
            pixels += bytes((b, g, r, alpha))  # BGRA

    # VTF 7.2 header, 80 bytes. Flags: CLAMPS | CLAMPT | NOMIP | NOLOD | EIGHTBITALPHA.
    flags = 0x0004 | 0x0008 | 0x0100 | 0x0200 | 0x2000
    header = struct.pack(
        "<4sIIIHHIHH4x3f4xfIBIBBH",
        b"VTF\0", 7, 2, 80, SIZE, SIZE, flags, 1, 0,
        0.5, 0.5, 0.5,      # reflectivity
        1.0,                # bump scale
        12,                 # IMAGE_FORMAT_BGRA8888
        1,                  # mip count
        0xFFFFFFFF,         # no low-res thumbnail
        0, 0,
        1,                  # depth
    )
    header += b"\0" * (80 - len(header))
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "wb") as f:
        f.write(header + pixels)
    print("wrote", os.path.normpath(OUT))


if __name__ == "__main__":
    main()
