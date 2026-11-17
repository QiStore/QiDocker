"""Create DSM icons from Docker's published Ocean Blue whale symbol."""
import pathlib
import subprocess
import sys

source = pathlib.Path(__file__).resolve().parents[1] / "spk/assets/docker-mark.png"
outer, ui = map(pathlib.Path, sys.argv[1:])
icons = [(outer, "PACKAGE_ICON.PNG", 72), (outer, "PACKAGE_ICON_256.PNG", 256)]
icons.extend((ui, f"icon_0004_{size}.png", size) for size in (16, 24, 32, 48, 64, 72, 128, 256))

for directory, name, size in icons:
    content_size = round(size * 0.8)
    subprocess.run([
        "convert", "-background", "none", str(source), "-trim", "+repage", "-resize", f"{content_size}x{content_size}",
        "-gravity", "center", "-extent", f"{size}x{size}", f"PNG32:{directory / name}",
    ], check=True)
