#!/usr/bin/env python3
"""Compare two directories of screenshots pixel by pixel (./scripts/android.sh shots, docs/android/android.md).

    python3 scripts/app/android_shots_diff.py <before> <after> [--diff <directory>]
        [--tolerance <n>] [--ignore '<name glob>=x0,y0,x1,y1' ...]

PNGs with the same name are compared as raw RGBA bytes. Never ImageChops.difference(a, b).getbbox(): for RGBA
images getbbox() only looks at alpha, which is 255 everywhere in a screenshot, so it always reports "same".
For every differing pair it prints how many pixels differ, their bounding box and the largest channel delta;
with --diff it writes the after image faded, with the differing pixels in magenta. Both loosening options are off
by default: --tolerance n lets pixels whose channels differ by at most n count as the same, --ignore skips a
rectangle (inclusive pixel coordinates) in the images whose names match the glob, e.g. a measured latency.
Decoding uses Pillow when it can be imported, else a small built-in decoder (8-bit, non-interlaced PNG; slower),
so the script also runs under `python3 -I`.
Exit status: 0 when every image matches and none is missing, 1 otherwise, 2 for bad arguments or unreadable files.
"""
import argparse
import fnmatch
from io import BytesIO
import struct
import sys
from tempfile import NamedTemporaryFile
import zlib
from pathlib import Path

PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"
MAGENTA = b"\xff\x00\xff\xff"
FADE = bytes(128 + v // 2 for v in range(256))  # toward white, keeps the page readable behind the marks


class Picture:
    def __init__(self, width, height, rgba):
        self.width, self.height, self.rgba = width, height, rgba


def load(data):
    try:
        from PIL import Image
    except ImportError:
        return decode_png(data)
    with Image.open(BytesIO(data)) as image:
        image = image.convert("RGBA")
        return Picture(image.width, image.height, image.tobytes())


def decode_png(data):
    """Decode an 8-bit, non-interlaced PNG (gray, RGB, gray + alpha or RGBA) to RGBA bytes."""
    if data[:8] != PNG_SIGNATURE:
        raise ValueError("不是 PNG 文件")
    pos, chunks, header = 8, [], None
    while pos + 8 <= len(data):
        length, kind = struct.unpack(">I4s", data[pos:pos + 8])
        body = data[pos + 8:pos + 8 + length]
        pos += 12 + length
        if kind == b"IHDR":
            header = struct.unpack(">IIBBBBB", body)
        elif kind == b"IDAT":
            chunks.append(body)
        elif kind == b"IEND":
            break
    if header is None:
        raise ValueError("缺少 IHDR")
    width, height, depth, color, _, _, interlace = header
    channels = {0: 1, 2: 3, 4: 2, 6: 4}.get(color)
    if depth != 8 or channels is None or interlace:
        raise ValueError("不支持的 PNG 格式（位深 %d、颜色类型 %d、隔行 %d），请安装 Pillow" % (depth, color, interlace))
    raw = zlib.decompress(b"".join(chunks))
    stride = width * channels
    pixels = bytearray(height * stride)
    previous = bytearray(stride)
    pos = 0
    for y in range(height):
        kind, line = raw[pos], bytearray(raw[pos + 1:pos + 1 + stride])
        pos += 1 + stride
        if kind == 1:
            for i in range(channels, stride):
                line[i] = (line[i] + line[i - channels]) & 255
        elif kind == 2:
            line = bytearray((a + b) & 255 for a, b in zip(line, previous))
        elif kind == 3:
            for i in range(stride):
                left = line[i - channels] if i >= channels else 0
                line[i] = (line[i] + (left + previous[i]) // 2) & 255
        elif kind == 4:
            for i in range(stride):
                a = line[i - channels] if i >= channels else 0
                b, c = previous[i], previous[i - channels] if i >= channels else 0
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                line[i] = (line[i] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
        elif kind != 0:
            raise ValueError("未知的 PNG 行过滤类型 %d" % kind)
        pixels[y * stride:(y + 1) * stride] = line
        previous = line
    if channels == 4:
        return Picture(width, height, bytes(pixels))
    rgba = bytearray(width * height * 4)
    if channels == 3:
        for i in range(3):
            rgba[i::4] = pixels[i::3]
        rgba[3::4] = b"\xff" * (width * height)
    else:  # gray, gray + alpha
        for i in range(3):
            rgba[i::4] = pixels[0::channels]
        rgba[3::4] = pixels[1::2] if channels == 2 else b"\xff" * (width * height)
    return Picture(width, height, bytes(rgba))


def write_png(path, width, height, rgba):
    stride = width * 4
    raw = b"".join(b"\x00" + rgba[y * stride:(y + 1) * stride] for y in range(height))

    def chunk(kind, body):
        return struct.pack(">I", len(body)) + kind + body + struct.pack(">I", zlib.crc32(kind + body) & 0xffffffff)
    data = (PNG_SIGNATURE + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)) +
            chunk(b"IDAT", zlib.compress(raw, 6)) + chunk(b"IEND", b""))
    temporary = None
    try:
        with NamedTemporaryFile(dir=path.parent, prefix=".screenshot-diff-", delete=False) as output:
            temporary = Path(output.name)
            output.write(data)
        temporary.replace(path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def compare(before, after, tolerance=0, ignored=()):
    """Count differing pixels outside the ignored boxes and above the tolerance.

    Returns (count, (x0, y0, x1, y1) or None, largest channel delta, marked offsets, tolerated, ignored)."""
    width, height, stride = before.width, before.height, before.width * 4
    a, b = memoryview(before.rgba), memoryview(after.rgba)
    count, delta, box, marks, tolerated, skipped = 0, 0, None, [], 0, 0
    for y in range(height):
        row_a, row_b = a[y * stride:(y + 1) * stride], b[y * stride:(y + 1) * stride]
        if row_a == row_b:
            continue
        pixels_a, pixels_b = row_a.cast("I"), row_b.cast("I")
        xs = []
        for x in range(width):
            if pixels_a[x] == pixels_b[x]:
                continue
            if any(x0 <= x <= x1 and y0 <= y <= y1 for x0, y0, x1, y1 in ignored):
                skipped += 1
                continue
            offset = x * 4
            pixel_delta = max(abs(row_a[offset + i] - row_b[offset + i]) for i in range(4))
            if pixel_delta <= tolerance:
                tolerated += 1
                continue
            delta = max(delta, pixel_delta)
            marks.append(y * stride + offset)
            xs.append(x)
        if not xs:
            continue
        count += len(xs)
        box = (xs[0], y, xs[-1], y) if box is None else (min(box[0], xs[0]), box[1], max(box[2], xs[-1]), y)
    return count, box, delta, marks, tolerated, skipped


def parse_ignore(value):
    try:
        pattern, rectangle = value.rsplit("=", 1)
        x0, y0, x1, y1 = (int(v) for v in rectangle.split(","))
    except ValueError:
        raise argparse.ArgumentTypeError("格式是 '<文件名通配>=x0,y0,x1,y1'：%s" % value)
    return pattern, (min(x0, x1), min(y0, y1), max(x0, x1), max(y0, y1))


def main():
    parser = argparse.ArgumentParser(description="逐像素比较两个截图目录里的同名 PNG。")
    parser.add_argument("before", type=Path, help="改前的截图目录")
    parser.add_argument("after", type=Path, help="改后的截图目录")
    parser.add_argument("--diff", type=Path, help="把差异图（改后的图变淡，不同的像素标成品红）写到这个目录")
    parser.add_argument("--tolerance", type=int, default=0, metavar="N",
                        help="每个通道差不超过 N 的像素算相同（默认 0，严格逐像素）")
    parser.add_argument("--ignore", type=parse_ignore, action="append", default=[], metavar="GLOB=x0,y0,x1,y1",
                        help="文件名匹配 GLOB 的图里不比这个矩形（含边界），可重复")
    args = parser.parse_args()
    for directory in (args.before, args.after):
        if not directory.is_dir():
            print("不是目录：%s" % directory, file=sys.stderr)
            return 2
    names_before = {p.name for p in args.before.glob("*.png")}
    names_after = {p.name for p in args.after.glob("*.png")}
    if args.diff:
        if args.diff.resolve() in (args.before.resolve(), args.after.resolve()):
            parser.error("--diff 不能指向原始截图目录")
        args.diff.mkdir(parents=True, exist_ok=True)
    same, different, unreadable = [], [], []
    for name in sorted(names_before & names_after):
        path_a, path_b = args.before / name, args.after / name
        try:
            data_a, data_b = path_a.read_bytes(), path_b.read_bytes()
            before = load(data_a)
            if data_a == data_b:
                same.append(name)
                print("相同  %s" % name)
                continue
            after = load(data_b)
        except (OSError, ValueError, zlib.error, struct.error, IndexError) as error:
            unreadable.append(name)
            print("读不了  %s：%s" % (name, error))
            continue
        if (before.width, before.height) != (after.width, after.height):
            different.append(name)
            print("不同  %s：尺寸不一样（改前 %d×%d，改后 %d×%d）" % (
                name, before.width, before.height, after.width, after.height))
            continue
        ignored = [box for pattern, box in args.ignore if fnmatch.fnmatch(name, pattern)]
        count, box, delta, marks, tolerated, skipped = compare(before, after, args.tolerance, ignored)
        loosened = "".join(note for value, note in ((tolerated, "，容差内 %d 个像素" % tolerated),
                                                   (skipped, "，忽略区域内 %d 个像素不同" % skipped)) if value)
        if not count:
            same.append(name)
            print("相同  %s（%s）" % (name, loosened[1:] if loosened else "文件字节不同，像素一样"))
            continue
        different.append(name)
        x0, y0, x1, y1 = box
        share = count * 100 / (before.width * before.height)
        print("不同  %s：%d 个像素（%.4f%%），范围 x %d–%d、y %d–%d（%d×%d），最大通道差 %d%s" % (
            name, count, share, x0, x1, y0, y1, x1 - x0 + 1, y1 - y0 + 1, delta, loosened))
        if args.diff:
            marked = bytearray(after.rgba.translate(FADE))
            for offset in marks:
                marked[offset:offset + 4] = MAGENTA
            write_png(args.diff / name, after.width, after.height, bytes(marked))
    only_before, only_after = sorted(names_before - names_after), sorted(names_after - names_before)
    for name in only_before:
        print("缺失  %s：只在改前目录里" % name)
    for name in only_after:
        print("缺失  %s：只在改后目录里" % name)
    print("合计：相同 %d，不同 %d，读不了 %d，只在改前 %d，只在改后 %d" % (
        len(same), len(different), len(unreadable), len(only_before), len(only_after)))
    if args.diff and different:
        print("差异图：%s" % args.diff)
    if unreadable:
        return 2
    return 0 if not (different or only_before or only_after) else 1


if __name__ == "__main__":
    sys.exit(main())
