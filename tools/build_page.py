# Fills the page template with embedded images so DPLocker.exe is one self-contained file.
import pathlib
root = pathlib.Path(__file__).resolve().parent.parent
page = (root / "index.template.html").read_text()
for token, source in {
    "__PHOTO__": "assets/photo.b64",
    "__LOGO_BLACK__": "assets/logo-black.b64",
    "__LOGO_WHITE__": "assets/logo-white.b64",
}.items():
    page = page.replace(token, (root / source).read_text().strip())
import re
symbols = []
for symbol_id, source, token in [("dpMark", "assets/mark.svg", "__MARK_VIEWBOX__"), ("dpWordmark", "assets/wordmark.svg", "__WORDMARK_VIEWBOX__")]:
    svg = (root / source).read_text()
    view_box = re.search(r'viewBox="([^"]+)"', svg).group(1)
    path = re.search(r'<path[^>]*/>', svg).group(0)
    symbols.append(f'<symbol id="{symbol_id}" viewBox="{view_box}">{path}</symbol>')
    page = page.replace(token, view_box)
page = page.replace("__LOGO_SYMBOLS__", "".join(symbols))
version = next(line for line in (root / "main.go").read_text().splitlines() if "appVersion" in line and '"' in line)
page = page.replace("__VERSION__", version.split('"')[1])
(root / "index.html").write_text(page)
