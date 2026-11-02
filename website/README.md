# kube-res-foresight 官网与文档

`website/` 保存网站编辑源，`site/` 保存可直接由 GitHub Pages 托管的页面。

## 内容组织

- 中英文首页：项目介绍、三项核心能力、功能流程、使用模式与文档入口。
- 中英文文档中心：开始使用、核心概念、算法参数手册、使用指南、AI 助手、运维与管理六组，每种语言包含 33 篇独立指南（含 15 篇算法与模型参数手册）。
- 系统架构与运行逻辑包含双语 SVG 总览、模块、执行路径与运行循环图，支持放大、全屏、原图查看和下载；图源在 `scripts/site_diagrams.py`。
- 每篇文章具备分组侧栏、页内目录、上一篇/下一篇、代码复制与源文件编辑入口。
- 顶部支持全文搜索、`Ctrl/Cmd + K`、语言切换和“跟随系统 / 浅色 / 深色”。
- 手机使用可展开的文档目录；禁用脚本时仍可阅读文章和浏览链接。


## 编辑与预览

需要 Python 3.12 或更新版本，无额外 Python 依赖：

```sh
python scripts/build-site.py
python scripts/check-site.py
python -m http.server 4173 --bind 127.0.0.1 --directory site
```

打开 `http://127.0.0.1:4173/`，文档入口为 `http://127.0.0.1:4173/docs/`。也可从仓库根目录启动服务器，使用 `/site/` 检查项目路径前缀。

| 修改内容 | 编辑位置 |
|---|---|
| 文章正文 | `website/content/zh/*.html`、`website/content/en/*.html` |
| 文章标题、摘要与分组 | `website/content/manifest.json` |
| 架构图布局、标签与生成 | `scripts/site_diagrams.py` |
| 架构图样式与缩放 | `website/assets/diagrams.css`、`website/assets/diagrams.js` |
| 首页内容与公共模板 | `scripts/build-site.py` |
| 样式、交互与图形 | `website/assets/` |
| 源码仓库地址与分支 | `site/assets/config.js` |
| 404 页面 | `site/404.html` |

正文使用 HTML 片段；标题用带显式 `id` 的 `h2`，两个语言版本保持相同锚点。代码块使用 `<pre data-language="Bash"><code>…</code></pre>`，生成时自动添加复制按钮。站内文章链接使用相对路径。

生成脚本会刷新首页、文档、共享资源与 `site/assets/search-index.json`，不要直接修改这些生成文件。`site/assets/config.js` 和 `site/404.html` 独立维护，不会被生成过程覆盖。

## 检查

```sh
python scripts/build-site.py
python scripts/check-site.py
node --check site/assets/main.js
node --check site/assets/locale.js
node --check site/assets/theme.js
node --check site/assets/legacy-docs.js
```

检查覆盖本地资源、锚点、交互目标、语言标记、中英文目录对应、算法数量与搜索索引。算法说明以当前目录为准；更新算法后同步维护两种语言。

更新算法参数手册时，从仓库根目录执行 `go run ./scripts/check-algorithm-docs`，核对当前注册目录的参数覆盖，并使用实际参数校验器检查双语 JSON 示例。该检查不连接模型服务、不执行预测。

## GitHub Pages

工作流位于 `.github/workflows/pages.yml`。在仓库 **Settings → Pages → Source** 中选择 **GitHub Actions**，将网站文件合入默认分支或手动运行工作流。

流程会先生成并检查页面，再上传 `site/`。PR 与非默认分支仅运行检查。Pages 的最终地址由 `github-pages` 环境返回。全部链接支持带仓库名的项目路径，不需要服务端路由。

原有 `docs.html#quickstart` 等入口会转到对应文章，文章内的语言切换保留当前路径与章节。404 首页链接由发布任务按实际 Pages 地址写入。

语言选择顺序为 URL 参数、保存的偏好、浏览器首选语言；中文变体使用中文，其余默认英文。外观默认跟随浏览器，手动选择后跨页面保存。
