"""Generate the small PNG icons read by DSM Package Center and launcher."""
import pathlib
import struct
import sys
import zlib


def chunk(tag, data):
    return struct.pack(">I", len(data)) + tag + data + struct.pack(">I", zlib.crc32(tag + data))


def make_icon(size):
    rows = bytearray()
    for y in range(size):
        rows.append(0)
        for x in range(size):
            u, v = (x + 0.5) / size, (y + 0.5) / size
            if (max(abs(u - 0.5) - .29, 0)**2 + max(abs(v - 0.5) - .29, 0)**2) > .16**2:
                pixel = (0, 0, 0, 0)
            elif (.22 < u < .78 and .26 < v < .73 and
                  (abs(u - .5) > .19 or abs(v - .5) > .14) or
                  .51 < u < .74 and .57 < v < .79 and abs((u - .51) - (v - .57)) < .06):
                pixel = (255, 255, 255, 255)
            else:
                pixel = (0, 126, 211, 255)
            rows.extend(pixel)
    header = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(rows)) + chunk(b"IEND", b"")


outer, ui = map(pathlib.Path, sys.argv[1:])
for directory, name, size in [(outer, "PACKAGE_ICON.PNG", 72), (outer, "PACKAGE_ICON_256.PNG", 256),
                              *[(ui, f"icon_{size}.png", size) for size in (16, 24, 32, 48, 64, 72, 128, 256)]]:
    (directory / name).write_bytes(make_icon(size))
