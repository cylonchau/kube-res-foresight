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
            expected = "en" if self.path.relative_to(ROOT).parts[0] == "en" else "zh-CN"
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
    algorithm_ids = re.findall(r'^\s*"([a-z_]+)":', builders.split("var builtinChecks", 1)[0], re.M)
    builtin_ids = [name for name in algorithm_ids if name != "model_service"]
    docs = (ROOT / "docs/algorithms.html").read_text(encoding="utf-8")
    if f"{len(builtin_ids)} 种算法" not in docs:
        errors.append("docs/algorithms.html: update documented algorithm count to match builtinBuilders")
    english_docs = (ROOT / "en/docs/algorithms.html").read_text(encoding="utf-8")
    if f"{len(builtin_ids)} built-in algorithms" not in english_docs:
        errors.append("en/docs/algorithms.html: update documented algorithm count to match builtinBuilders")
    for algorithm in algorithm_ids:
        for prefix in ("docs", "en/docs"):
            reference = ROOT / prefix / f"algorithm-{algorithm}.html"
            if not reference.is_file():
                errors.append(f"missing algorithm parameter guide: {reference.relative_to(ROOT)}")
            elif f'algorithm-{algorithm}.html' not in (ROOT / prefix / "algorithms.html").read_text(encoding="utf-8"):
                errors.append(f"algorithm overview does not link to {algorithm}: {prefix}")
    for zh_path in (ROOT / "docs").glob("*.html"):
        en_path = ROOT / "en/docs" / zh_path.name
        if not en_path.is_file():
            errors.append(f"missing English document: {en_path.relative_to(ROOT)}")
            continue
        zh_sections = re.findall(r'<h2 id="([^"]+)"', zh_path.read_text(encoding="utf-8"))
        en_sections = re.findall(r'<h2 id="([^"]+)"', en_path.read_text(encoding="utf-8"))
        if zh_sections != en_sections:
            errors.append(f"documentation anchors differ between languages: {zh_path.name}")
    for path in pages:
        content = path.read_text(encoding="utf-8")
        if re.search(r"platform-progress|执行进度|本次官网交付|完整证据|集群验收|本地测试床", content):
            errors.append(f"{path.relative_to(ROOT)}: internal delivery or test language in public content")
        if re.search(r"macd_trend|MACD Trend|Naive Bayes|RNN / GRU / LSTM|纯 Go|pure Go", content):
            errors.append(f"{path.relative_to(ROOT)}: retired model or unwanted implementation-language copy")
    import json
    index = json.loads((ROOT / "assets/search-index.json").read_text(encoding="utf-8"))
    for item in index:
        if not (ROOT / item["path"]).is_file() or item["lang"] not in ("zh", "en") or not item["text"]:
            errors.append(f"invalid search entry: {item['path']}")
    for stylesheet in (ROOT / "assets").glob("*.css"):
        for target in re.findall(r'url\([\"\']?([^\"\')]+)', stylesheet.read_text(encoding="utf-8")):
            if not urlsplit(target).scheme and not (stylesheet.parent / target).is_file():
                errors.append(f"missing stylesheet asset: {target}")
    for required in (".nojekyll", "index.html", "docs/index.html", "docs.html", "en/index.html", "en/docs/index.html", "en/docs.html", "assets/search-index.json", "assets/legacy-docs.js", "assets/main.js", "assets/locale.js", "assets/theme.js", "assets/style.css", "assets/adaptive.css", "assets/light.css", "assets/config.js"):
        if not (ROOT / required).is_file():
            errors.append(f"missing published file: {required}")
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    print(f"Website checks passed: {len(pages)} HTML pages, local resources, anchors, control targets, {len(builtin_ids)} builtin algorithms and model service guides.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
