"""Build the public website and bilingual documentation with the Python standard library."""
from pathlib import Path, PurePosixPath
from html import escape, unescape
from html.parser import HTMLParser
import json
import posixpath
import re

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "website/content"
OUTPUT = ROOT / "site"
MANIFEST = json.loads((SOURCE / "manifest.json").read_text(encoding="utf-8"))
REPO_MATCH = re.search(r"repository:\s*['\"]([^'\"]+)", (OUTPUT / "assets/config.js").read_text(encoding="utf-8"))
REPO = REPO_MATCH.group(1) if REPO_MATCH else "https://github.com/cylonchau/kube-resource-fcst"
PAGE_IDS = [page for group in MANIFEST["groups"] for page in group["pages"]]
ICONS = {
    "search": '<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 4 4"/>',
    "arrow": '<path d="M5 12h14m-5-5 5 5-5 5"/>',
    "book": '<path d="M4 4h6a3 3 0 0 1 2 1 3 3 0 0 1 2-1h6v15h-6a3 3 0 0 0-2 1 3 3 0 0 0-2-1H4Zm8 1v15"/>',
    "chart": '<path d="M4 3v17h17M7 15l4-5 4 2 5-7"/>',
    "sliders": '<path d="M4 7h16M4 17h16M8 4v6m8 4v6"/>',
    "shield": '<path d="m12 3 8 3v6c0 5-8 9-8 9s-8-4-8-9V6Zm-4 9 3 3 5-6"/>',
    "layers": '<path d="m12 3 9 5-9 5-9-5Zm-9 9 9 5 9-5M3 16l9 5 9-5"/>',
    "clock": '<circle cx="12" cy="12" r="9"/><path d="M12 7v6l4 2"/>',
    "github": '<path d="M8 20c-4 1-4-2-5-2m14 4v-4a3 3 0 0 0-1-2c3 0 5-2 5-5a6 6 0 0 0-2-4c0-1 0-3-1-3s-3 1-4 1a12 12 0 0 0-4 0C9 5 7 3 6 4S5 6 6 7a6 6 0 0 0-2 4c0 3 2 5 5 5a3 3 0 0 0-1 2v4"/>',
}


def icon(name):
    return f'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">{ICONS[name]}</svg>'


def label(lang, zh, en):
    return zh if lang == "zh" else en


def route(lang, page):
    return ("en/" if lang == "en" else "") + page


def relative(current, target):
    return posixpath.relpath(target, str(PurePosixPath(current).parent))


def page_link(current, lang, page, fragment=""):
    return relative(current, route(lang, page)) + fragment


class Text(HTMLParser):
    def __init__(self):
        super().__init__()
        self.parts = []

    def handle_data(self, data):
        self.parts.append(data)


def plain(value):
    parser = Text()
    parser.feed(value)
    return " ".join(" ".join(parser.parts).split())


def header(current, lang, page, docs=False):
    home = page_link(current, lang, "index.html")
    doc = page_link(current, lang, "docs/index.html")
    start = page_link(current, lang, "docs/getting-started.html")
    choices = "".join(f'<button type="button" data-theme-mode="{mode}" aria-pressed="{str(mode == "auto").lower()}"><span aria-hidden="true">{symbol}</span>{label(lang, zh, en)}</button>' for mode, symbol, zh, en in [("auto", "◐", "跟随系统", "System"), ("light", "☀", "浅色", "Light"), ("dark", "☾", "深色", "Dark")])
    languages = "".join(f'<a href="{relative(current, route(code, page))}?lang={code}" data-language="{code}" lang="{html_lang}"{(" aria-current=\"true\"" if code == lang else "")}>{title}</a>' for code, html_lang, title in [("zh", "zh-CN", "中文"), ("en", "en", "EN")])
    return f'''<header class="site-header"><div class="header-inner">
<a class="brand" href="{home}" aria-label="kube-resource-fcst {label(lang, '首页', 'home')}"><img src="{relative(current, 'assets/favicon.svg')}" width="30" height="30" alt=""><span>kube-resource-fcst</span></a>
<nav id="site-nav" aria-label="{label(lang, '主导航', 'Main navigation')}"><a href="{home}"{(' aria-current="page"' if not docs else '')}>{label(lang, '首页', 'Home')}</a><a href="{doc}"{(' aria-current="page"' if docs else '')}>{label(lang, '文档', 'Documentation')}</a><a href="{start}">{label(lang, '快速开始', 'Quick start')}</a><a href="{REPO}" data-repo>GitHub ↗</a></nav>
<div class="header-tools"><button class="search-trigger" type="button" data-search-open aria-label="{label(lang, '搜索文档', 'Search documentation')}">{icon('search')}<span>{label(lang, '搜索文档', 'Search docs')}</span><kbd>⌘ K</kbd></button>
<div class="language-switch" role="group" aria-label="{label(lang, '选择语言', 'Language')}">{languages}</div>
<div class="theme-control"><button type="button" class="theme-toggle" id="theme-toggle" aria-expanded="false" aria-controls="theme-menu" aria-label="{label(lang, '切换外观', 'Appearance')}"><span aria-hidden="true">◐</span></button><div id="theme-menu" class="theme-menu" role="group" aria-label="{label(lang, '外观', 'Appearance')}" hidden>{choices}</div></div>
<button type="button" class="menu-toggle" aria-label="{label(lang, '展开导航', 'Toggle navigation')}" aria-expanded="false" aria-controls="site-nav"><span aria-hidden="true">☰</span></button></div></div></header>'''


def footer(current, lang):
    return f'''<footer class="site-footer"><div class="container footer-grid"><div><a class="brand" href="{page_link(current, lang, 'index.html')}"><img src="{relative(current, 'assets/favicon.svg')}" alt="" width="28" height="28"><span>kube-resource-fcst</span></a><p>{label(lang, '预测需求，合理配置资源。', 'Forecast demand. Allocate resources with confidence.')}</p></div><div><strong>{label(lang, '了解项目', 'Explore')}</strong><a href="{page_link(current, lang, 'docs/concepts.html')}">{label(lang, '核心概念', 'Core concepts')}</a><a href="{page_link(current, lang, 'docs/algorithms.html')}">{label(lang, '算法管理', 'Models')}</a></div><div><strong>{label(lang, '开始使用', 'Get started')}</strong><a href="{page_link(current, lang, 'docs/getting-started.html')}">{label(lang, '快速开始', 'Quick start')}</a><a href="{page_link(current, lang, 'docs/installation.html')}">{label(lang, '安装与部署', 'Installation')}</a></div><div><strong>{label(lang, '参与项目', 'Project')}</strong><a data-repo href="{REPO}">GitHub ↗</a><a data-issues href="{REPO}/issues">{label(lang, '问题反馈', 'Report an issue')} ↗</a></div></div><div class="container footer-bottom"><span>© 2026 kube-resource-fcst</span><span>{label(lang, '面向 Kubernetes 的资源预测与弹性调度', 'Resource forecasting and scaling for Kubernetes')}</span></div></footer>'''


def search_dialog(lang):
    return f'''<dialog id="search-dialog" aria-labelledby="search-title"><div class="search-dialog-head"><span id="search-title">{label(lang, '搜索文档', 'Search documentation')}</span><button type="button" data-search-close aria-label="{label(lang, '关闭搜索', 'Close search')}">Esc</button></div><div class="search-input-wrap">{icon('search')}<input id="doc-search" type="search" autocomplete="off" placeholder="{label(lang, '搜索功能、配置或使用问题…', 'Find a feature, setting, or question…')}" aria-label="{label(lang, '搜索关键词', 'Search keywords')}" aria-describedby="search-status"></div><p id="search-status" role="status"></p><ul id="search-results" aria-label="{label(lang, '搜索结果', 'Search results')}"></ul><div class="search-help">{label(lang, '↑ ↓ 选择 · Enter 打开 · Esc 关闭', '↑ ↓ Select · Enter Open · Esc Close')}</div></dialog>'''


def base(current, lang, page, title, description, content, docs=False):
    prefix = relative(current, "index.html").removesuffix("index.html") or "./"
    asset = lambda name: relative(current, f"assets/{name}")
    html_lang = "zh-CN" if lang == "zh" else "en"
    return f'''<!doctype html>
<html lang="{html_lang}" data-site-root="{prefix}" data-page-path="{page}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="theme-color" content="#245fd4"><meta name="description" content="{escape(description, quote=True)}"><meta property="og:title" content="{escape(title, quote=True)}"><meta property="og:description" content="{escape(description, quote=True)}"><meta property="og:type" content="website"><title>{escape(title)}</title>
<script src="{asset('locale.js')}"></script><link rel="alternate" hreflang="zh-CN" href="{relative(current, route('zh', page))}"><link rel="alternate" hreflang="en" href="{relative(current, route('en', page))}"><link rel="icon" type="image/svg+xml" href="{asset('favicon.svg')}"><link rel="stylesheet" href="{asset('style.css')}"><link rel="stylesheet" href="{asset('adaptive.css')}"><link id="light-theme" rel="stylesheet" href="{asset('light.css')}" media="(prefers-color-scheme: light)"><script src="{asset('theme.js')}"></script><script src="{asset('config.js')}" defer></script><script src="{asset('main.js')}" defer></script></head>
<body class="{'documentation-page' if docs else 'home-page'}"><a class="skip-link" href="#main">{label(lang, '跳到主要内容', 'Skip to content')}</a>{header(current, lang, page, docs)}{content}{footer(current, lang)}{search_dialog(lang)}</body></html>\n'''


def home(lang):
    current = route(lang, "index.html")
    link = lambda page, fragment="": page_link(current, lang, page, fragment)
    title = label(lang, "让资源配置，跟上业务节奏。", "Resource decisions that move with demand.")
    description = label(lang, "面向单 Kubernetes 集群的资源预测与弹性调度平台。连接指标、比较预测、审查建议，让资源调整有据可循。", "Resource forecasting and scaling for a single Kubernetes cluster. Connect metrics, compare forecasts, and understand every recommendation.")
    pillars = [
        ("chart", "看清资源需求", "Understand demand", "把 CPU、内存与请求量等指标连接到工作负载，理解历史规律，预测未来变化。", "Connect workload metrics, understand historical patterns, and forecast changes in demand.", "metrics"),
        ("sliders", "形成合理建议", "Review resource advice", "用回测与实测比较算法，结合前瞻共识、资源边界和保护策略给出建议。", "Compare models and combine look-ahead consensus with constraints and safeguards.", "recommendations"),
        ("shield", "逐步开放执行", "Enable bounded actions", "从观察、推荐开始，分别授权资源准入和副本伸缩，持续追踪实际效果。", "Start with observation, then authorize admission and replica scaling while reviewing outcomes.", "scaling"),
    ]
    features = [
        ("layers", "工作负载模块", "Workload modules", "一个工作负载，一份指标、周期、边界与兜底策略。", "One workload with its own metrics, intervals, bounds, and fallback.", "concepts"),
        ("chart", "多算法预测与回测", "Forecasting and backtests", "统一管理预测方法，在相同窗口下比较资源表现。", "Manage methods centrally and compare resource performance on shared windows.", "algorithms"),
        ("clock", "前瞻资源决策", "Look-ahead decisions", "把容量就绪时间纳入考虑，综合多个候选形成共识。", "Account for readiness time and combine multiple candidates into consensus.", "recommendations"),
        ("sliders", "固定周期与回退", "Schedules and fallback", "为明确时段配置保障规则，异常时按策略回退。", "Define business windows and explicit behavior when inputs fail.", "safety"),
        ("shield", "资源保护与执行边界", "Resource safeguards", "检查 OOM、上下界、步长、冷却及副本控制冲突。", "Respect OOM protection, bounds, steps, cooldowns, and replica ownership.", "scaling"),
        ("book", "监控与审计追溯", "Monitoring and audit", "串起预测、推荐、准入与伸缩，解释每一次资源变化。", "Trace each change across forecasts, recommendations, admission, and scaling.", "monitoring"),
    ]
    pillars_html = "".join(f'<article class="pillar"><div class="pillar-icon">{icon(i)}</div><h3>{label(lang, zh, en)}</h3><p>{label(lang, zhd, end)}</p><a class="text-link" href="{link("docs/"+page+".html")}">{label(lang, "了解更多", "Learn more")} {icon("arrow")}</a></article>' for i, zh, en, zhd, end, page in pillars)
    features_html = "".join(f'<a class="feature-item" href="{link("docs/"+page+".html")}"><span class="feature-icon">{icon(i)}</span><div><h3>{label(lang, zh, en)}</h3><p>{label(lang, zhd, end)}</p></div>{icon("arrow")}</a>' for i, zh, en, zhd, end, page in features)
    flow = [("01", "指标接入", "Connect metrics", "指标模板 · 数据质量", "Templates · Data quality"), ("02", "预测与评估", "Forecast & compare", "多算法 · 滚动回测", "Models · Rolling backtests"), ("03", "推荐与共识", "Recommend", "提前量 · 资源约束", "Lead time · Constraints"), ("04", "执行与反馈", "Act & review", "资源准入 · 副本伸缩", "Admission · Scaling")]
    flow_html = "".join(f'<div class="flow-node"><span>{number}</span><h3>{label(lang, zh, en)}</h3><p>{label(lang, zhd, end)}</p></div>' for number, zh, en, zhd, end in flow)
    content = f'''<main id="main"><section class="hero"><div class="hero-grid" aria-hidden="true"></div><div class="container hero-content"><p class="eyebrow">KUBERNETES RESOURCE FORECASTING</p><h1>kube-resource-fcst</h1><p class="hero-tagline">{title}</p><p class="hero-description">{label(lang, '从历史指标到未来需求，从资源建议到弹性执行。', 'From historical metrics to future demand. From advice to controlled scaling.')}</p><div class="hero-actions"><a class="button primary" href="{link('docs/getting-started.html')}">{label(lang, '开始使用', 'Get started')} {icon('arrow')}</a><a class="button hero-secondary" href="{REPO}" data-repo>{icon('github')}{label(lang, '参与项目', 'Explore on GitHub')}</a></div><a class="hero-down" href="#about" aria-label="{label(lang, '了解项目', 'About the project')}">↓</a></div></section>
<section id="about" class="intro-band"><div class="container"><h2>{label(lang, 'kube-resource-fcst 是什么？', 'What is kube-resource-fcst?')}</h2><p>{description}</p></div></section>
<section class="container section pillars">{pillars_html}</section>
<section id="capabilities" class="section surface-section"><div class="container"><div class="section-heading"><p class="eyebrow">CAPABILITIES</p><h2>{label(lang, '围绕工作负载，连接每一项能力', 'A connected workflow for every workload')}</h2><p>{label(lang, '管理输入，理解预测，控制动作，再用实际表现持续评估。', 'Manage inputs, understand forecasts, bound actions, and keep evaluating actual outcomes.')}</p></div><div class="features-grid">{features_html}</div></div></section>
<section id="architecture" class="container section"><div class="section-heading"><p class="eyebrow">HOW IT WORKS</p><h2>{label(lang, '从指标到执行，形成反馈闭环', 'From metrics to action, with a feedback loop')}</h2><p>{label(lang, '以模块为中心组织指标和策略，每个阶段都能查看来源与结果。', 'Organize metrics and policies around a module, with clear inputs and outcomes at every stage.')}</p></div><div class="home-flow">{flow_html}</div><div class="flow-foundation"><span>{label(lang, '权限管理', 'Access control')}</span><span>{label(lang, '周期任务', 'Scheduled tasks')}</span><span>{label(lang, '安全保护', 'Safeguards')}</span><span>{label(lang, '操作审计', 'Audit')}</span></div><a class="text-link centered" href="{link('docs/concepts.html')}">{label(lang, '了解功能架构', 'Explore the concepts')} {icon('arrow')}</a></section>
<section class="surface-section section"><div class="container rollout"><div><p class="eyebrow">GRADUAL AUTOMATION</p><h2>{label(lang, '从观察开始，按需走向自动化', 'Start with observation. Automate at your pace.')}</h2><p>{label(lang, '先比较预测与实际表现，再审查资源建议。资源准入与副本执行分别授权，各自受边界约束。', 'Compare forecasts with actuals, then review resource advice. Authorize admission and scaling separately, within their respective bounds.')}</p><a class="text-link" href="{link('docs/safety.html')}">{label(lang, '安全与回退机制', 'Safety and fallback')} {icon('arrow')}</a></div><ol class="mode-list"><li><b>01</b><span>Observe<small>{label(lang, '观察历史与预测', 'Observe history and forecasts')}</small></span></li><li><b>02</b><span>Recommend<small>{label(lang, '审查可解释的建议', 'Review explainable recommendations')}</small></span></li><li><b>03</b><span>Admission / Auto<small>{label(lang, '分别开放资源与副本执行', 'Authorize resource and replica actions')}</small></span></li></ol></div></section>
<section class="container section docs-entry"><div class="section-heading"><p class="eyebrow">DOCUMENTATION</p><h2>{label(lang, '找到你的下一步', 'Find your next step')}</h2><p>{label(lang, '从第一次接入到日常运维，按场景阅读。', 'Guides for your first workload and everyday operations.')}</p></div><div class="entry-grid">{''.join(f'<a href="{link("docs/"+page+".html")}"><span>0{n}</span><h3>{label(lang, zh, en)}</h3><p>{label(lang, zhd, end)}</p>{icon("arrow")}</a>' for n, page, zh, en, zhd, end in [(1,'getting-started','快速开始','Quick start','体验演示，接入第一个工作负载。','Explore the demo and connect your first workload.'),(2,'installation','安装与部署','Installation','了解组件、配置与启动方式。','Prepare components, settings, and startup.'),(3,'monitoring','监控与排查','Monitoring','沿着流程定位问题、复盘变化。','Trace issues and review resource changes.')])}</div></section>
<section class="closing"><div class="container"><h2>{label(lang, '从一个工作负载开始。', 'Start with one workload.')}</h2><p>{label(lang, '用可信指标理解需求，用明确边界管理变化。', 'Understand demand with reliable metrics. Manage changes with clear bounds.')}</p><a class="button primary" href="{link('docs/index.html')}">{label(lang, '阅读项目文档', 'Read the documentation')} {icon('arrow')}</a></div></section></main>'''
    return base(current, lang, "index.html", f"kube-resource-fcst · {title}", description, content)


def sidebar(current, lang, page_id):
    groups = []
    for group in MANIFEST["groups"]:
        links = "".join(f'<a href="{page_link(current, lang, "docs/"+page+".html")}"{(" aria-current=\"page\" class=\"current\"" if page == page_id else "")}>{escape(MANIFEST["pages"][page][lang][0])}</a>' for page in group["pages"])
        groups.append(f'<details class="nav-group" open><summary>{group[lang]}</summary><div>{links}</div></details>')
    return f'<aside id="docs-sidebar" class="docs-sidebar" aria-label="{label(lang, "文档导航", "Documentation navigation")}"><a class="sidebar-title" href="{page_link(current, lang, "docs/index.html")}">{icon("book")}{label(lang, "项目文档", "Documentation")}</a>{"".join(groups)}</aside>'


def format_content(body, lang):
    def code_block(match):
        number = code_block.number
        code_block.number += 1
        code_id = f"code-{number}"
        return f'<div class="code-block"><div class="code-header"><span>{escape(match.group(1))}</span><button type="button" data-copy="{code_id}" aria-label="{label(lang, "复制代码", "Copy code")}">{label(lang, "复制", "Copy")}</button></div><pre><code id="{code_id}">{match.group(2)}</code></pre></div>'
    code_block.number = 1
    body = re.sub(r'<pre data-language="([^"]+)"><code>(.*?)</code></pre>', code_block, body, flags=re.S)
    return body.replace("<table>", '<div class="table-wrap"><table>').replace("</table>", "</table></div>")


def docs(lang, page_id=None):
    page = f"docs/{page_id or 'index'}.html"
    current = route(lang, page)
    if page_id:
        title, summary = MANIFEST["pages"][page_id][lang]
        body = format_content((SOURCE / lang / f"{page_id}.html").read_text(encoding="utf-8"), lang)
        group_title = next(group[lang] for group in MANIFEST["groups"] if page_id in group["pages"])
    else:
        title = label(lang, "文档中心", "Documentation")
        summary = label(lang, "了解功能，连接指标，管理资源。找到适合当前阶段的使用指南。", "Understand the capabilities, connect your metrics, and manage resources. Find the guide for your next step.")
        group_title = label(lang, "欢迎", "Welcome")
        body = f'<div class="docs-welcome">{icon("book")}<div><strong>{label(lang, "第一次使用？", "New to the project?")}</strong><p>{label(lang, "从快速开始体验完整流程，或先阅读核心概念。", "Start with a quick walkthrough or explore the core concepts first.")}</p><a class="text-link" href="getting-started.html">{label(lang, "开始阅读", "Start reading")} →</a></div></div>'
        for group in MANIFEST["groups"]:
            body += f'<section class="docs-category"><h2 id="group-{group["id"]}">{group[lang]}</h2><div class="article-links">'
            for item in group["pages"]:
                heading, description = MANIFEST["pages"][item][lang]
                body += f'<a href="{item}.html"><div><h3>{heading}</h3><p>{description}</p></div>{icon("arrow")}</a>'
            body += "</div></section>"
    headings = [(identifier, plain(heading)) for identifier, heading in re.findall(r'<h2 id="([^"]+)">(.*?)</h2>', body, re.S)]
    toc = f'<aside class="page-toc" aria-label="{label(lang, "本页目录", "On this page")}"><strong>{label(lang, "本页内容", "On this page")}</strong><nav>{"".join(f"<a href=\"#{identifier}\">{escape(heading)}</a>" for identifier, heading in headings)}</nav><a class="back-top" href="#main">{label(lang, "返回顶部", "Back to top")} ↑</a></aside>'
    pager = ""
    if page_id:
        index = PAGE_IDS.index(page_id)
        prev = PAGE_IDS[index - 1] if index > 0 else None
        nxt = PAGE_IDS[index + 1] if index + 1 < len(PAGE_IDS) else None
        pager = '<nav class="article-pager" aria-label="'+label(lang, "相邻文章", "Adjacent articles")+'">'
        for target, before in [(prev, True), (nxt, False)]:
            target_title = MANIFEST["pages"][target][lang][0] if target else label(lang, "文档中心", "Documentation")
            target_file = (target or "index") + ".html"
            pager += f'<a href="{target_file}" class="{"previous" if before else "next"}"><small>{label(lang, "上一篇" if before else "下一篇", "Previous" if before else "Next")}</small><span>{"← " if before else ""}{target_title}{" →" if not before else ""}</span></a>'
        pager += "</nav>"
    edit = f'<a class="edit-page" data-source="website/content/{lang}/{page_id}.html" href="{REPO}">{label(lang, "在 GitHub 上编辑此页", "Edit this page on GitHub")} ↗</a>' if page_id else ""
    mobile = f'<div class="docs-mobilebar"><button type="button" class="docs-nav-toggle" aria-expanded="false" aria-controls="docs-sidebar">☰ {label(lang, "文档目录", "Contents")}</button><button type="button" data-search-open>{icon("search")}{label(lang, "搜索", "Search")}</button></div><button class="sidebar-backdrop" type="button" aria-label="{label(lang, "关闭目录", "Close navigation")}" hidden></button>'
    content = f'''{mobile}<div class="docs-layout">{sidebar(current, lang, page_id)}<main id="main" class="docs-content"><nav class="breadcrumbs" aria-label="{label(lang, '当前位置', 'Breadcrumbs')}"><a href="{page_link(current, lang, 'docs/index.html')}">{label(lang, '文档', 'Docs')}</a><span aria-hidden="true">/</span><span>{group_title}</span></nav><div class="article-heading"><p class="eyebrow">{label(lang, '使用指南', 'USER GUIDE')}</p><h1>{escape(title)}</h1><p>{escape(summary)}</p></div><article class="article-body">{body}</article>{edit}{pager}</main>{toc}</div>'''
    search = {"lang": lang, "path": current, "title": title, "summary": summary, "text": plain(body)}
    return base(current, lang, page, f"{title} | kube-resource-fcst", summary, content, True), search


def legacy(lang):
    current = route(lang, "docs.html")
    target = page_link(current, lang, "docs/index.html")
    links = "".join(f'<li><a href="{page_link(current, lang, "docs/"+item+".html")}">{MANIFEST["pages"][item][lang][0]}</a></li>' for item in PAGE_IDS)
    content = f'<main id="main" class="container legacy-page"><h1>{label(lang, "项目文档", "Documentation")}</h1><p><a class="button primary" href="{target}">{label(lang, "进入文档中心", "Open documentation")} →</a></p><ul>{links}</ul></main>'
    html = base(current, lang, "docs.html", "Documentation | kube-resource-fcst", label(lang, "项目使用指南", "Project user guide"), content, True)
    return html.replace('<body class="documentation-page">', '<body class="documentation-page"><script src="'+relative(current, "assets/legacy-docs.js")+'"></script>', 1)


def write(path, content):
    destination = OUTPUT / path
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(content, encoding="utf-8")


def main():
    for asset in (ROOT / "website/assets").iterdir():
        if asset.is_file():
            write("assets/" + asset.name, asset.read_text(encoding="utf-8"))
    index = []
    for lang in ("zh", "en"):
        write(route(lang, "index.html"), home(lang))
        write(route(lang, "docs.html"), legacy(lang))
        for page_id in [None, *PAGE_IDS]:
            html, search = docs(lang, page_id)
            write(route(lang, f"docs/{page_id or 'index'}.html"), html)
            index.append(search)
    write("assets/search-index.json", json.dumps(index, ensure_ascii=False, separators=(",", ":")))
    print(f"Built 2 homepages, 2 compatibility entries and {len(index)} documentation pages.")


if __name__ == "__main__":
    main()
