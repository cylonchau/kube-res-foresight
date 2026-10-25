"""Validate the Pages artifact with Python's standard library; no app dependencies."""
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urlsplit
import re
import sys

ROOT = Path(__file__).resolve().parents[1] / "site"


class Page(HTMLParser):
    def __init__(self, path):
        super().__init__(convert_charrefs=True)
        self.path = path
        self.ids = set()
        self.links = []
        self.errors = []
        self.has_lang = False
        self.has_title = False

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "html":
            expected = "en" if self.path.parent.name == "en" else "zh-CN"
            self.has_lang = attrs.get("lang") == expected
        if tag == "title":
            self.has_title = True
        if "id" in attrs:
            if attrs["id"] in self.ids:
                self.errors.append(f"duplicate id: {attrs['id']}")
            self.ids.add(attrs["id"])
        for key in ("href", "src"):
            if attrs.get(key):
                self.links.append(attrs[key])
        for key in ("data-copy", "aria-controls"):
            if attrs.get(key):
                self.links.append("#" + attrs[key])


def main():
    pages = {}
    errors = []
    for path in sorted(ROOT.rglob("*.html")):
        page = Page(path)
        page.feed(path.read_text(encoding="utf-8"))
        pages[path] = page
    for path, page in pages.items():
        label = path.relative_to(ROOT)
        if not page.has_lang or not page.has_title:
            errors.append(f"{label}: missing or incorrect page language or title")
        errors.extend(f"{label}: {error}" for error in page.errors)
        for link in page.links:
            parsed = urlsplit(link)
            if parsed.scheme or parsed.netloc:
                continue
            if parsed.path.startswith("/"):
                errors.append(f"{label}: absolute local path breaks project Pages: {link}")
                continue
            target = (path.parent / unquote(parsed.path)).resolve() if parsed.path else path
            if not target.is_relative_to(ROOT):
                errors.append(f"{label}: link escapes published directory: {link}")
                continue
            if target.is_dir():
                target /= "index.html"
            if not target.is_file():
                errors.append(f"{label}: missing local target: {link}")
            elif parsed.fragment and target in pages and unquote(parsed.fragment) not in pages[target].ids:
                errors.append(f"{label}: missing anchor: {link}")
    # Catch accidental source-code drift when the public algorithm count changes.
    builders = (ROOT.parent / "pkg/forecast/builders.go").read_text(encoding="utf-8")
    algorithm_ids = re.findall(r'^\s*"([a-z_]+)":', builders.split("func recurrent(", 1)[0], re.M)
    docs = (ROOT / "docs.html").read_text(encoding="utf-8")
    if f"{len(algorithm_ids)} 种算法" not in docs:
        errors.append("docs.html: update documented algorithm count to match builtinBuilders")
    english_docs = (ROOT / "en/docs.html").read_text(encoding="utf-8")
    if f"{len(algorithm_ids)} built-in algorithms" not in english_docs:
        errors.append("en/docs.html: update documented algorithm count to match builtinBuilders")
    zh_sections = re.findall(r'<section id="([^"]+)"', docs)
    en_sections = re.findall(r'<section id="([^"]+)"', english_docs)
    if zh_sections != en_sections:
        errors.append("documentation chapter anchors differ between languages")
    for path in pages:
        content = path.read_text(encoding="utf-8")
        if re.search(r"platform-progress|执行进度|本次官网交付|完整证据|集群验收|本地测试床", content):
            errors.append(f"{path.relative_to(ROOT)}: internal delivery or test language in public content")
    for required in (".nojekyll", "index.html", "docs.html", "en/index.html", "en/docs.html", "assets/main.js", "assets/locale.js", "assets/theme.js", "assets/style.css", "assets/adaptive.css", "assets/light.css", "assets/config.js"):
        if not (ROOT / required).is_file():
            errors.append(f"missing published file: {required}")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"Website checks passed: {len(pages)} HTML pages, local resources, anchors, control targets, {len(algorithm_ids)} builtin algorithms.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
