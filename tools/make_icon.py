# Writes a standard Windows .ico: 32-bit BMP (DIB + AND mask) frames for small sizes,
# PNG only for 256px, planes=1 and bitcount=32 in every directory entry.
import io, struct, sys
from PIL import Image

SIZES = [16, 20, 24, 32, 40, 48, 64, 96, 128, 256]

def bmp_frame(image):
    size = image.width
    pixels = image.convert("RGBA").transpose(Image.FLIP_TOP_BOTTOM).tobytes("raw", "BGRA")
    header = struct.pack("<IiiHHIIiiII", 40, size, size * 2, 1, 32, 0, len(pixels), 0, 0, 0, 0)
    mask_row = ((size + 31) // 32) * 4
    return header + pixels + bytes(mask_row * size)

def png_frame(image):
    buffer = io.BytesIO()
    image.save(buffer, "PNG")
    return buffer.getvalue()

source = Image.open(sys.argv[1]).convert("RGBA")
frames = []
for size in SIZES:
    image = source.resize((size, size), Image.LANCZOS)
    frames.append((size, png_frame(image) if size == 256 else bmp_frame(image)))

output = bytearray(struct.pack("<HHH", 0, 1, len(frames)))
offset = 6 + 16 * len(frames)
for size, data in frames:
    dimension = 0 if size == 256 else size
    output += struct.pack("<BBBBHHII", dimension, dimension, 0, 0, 1, 32, len(data), offset)
    offset += len(data)
for _, data in frames:
    output += data
open(sys.argv[2], "wb").write(output)
