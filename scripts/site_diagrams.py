"""Original, bilingual SVG architecture figures for the buildless documentation site."""
from html import escape

DIAGRAMS = {
    "overview": ("系统总览", "System overview", "指标、平台、推荐、执行与实测反馈之间的关系。", "Metrics, the platform, advice, execution, and observed feedback."),
    "modules": ("功能模块架构", "Functional modules", "平台内的配置、预测、任务、接口与共享存储。", "Configuration, forecasting, jobs, interfaces, and shared storage."),
    "cycles": ("预测与反馈循环", "Forecast and feedback cycles", "预测周期、实测回填与按需刷新分别运行。", "Forecasting, actual-value evaluation, and on-demand refresh run separately."),
    "execution": ("资源与副本执行路径", "Resource and replica execution", "准入设置新 Pod 资源，Operator 单独控制副本。", "Admission configures new Pods; the Operator separately controls replicas."),
    "dashboard-flow": ("资源大盘的数据与结果", "What the resource dashboard reads and shows", "使用量来自 Prometheus，Pod 配置与副本数来自 Kubernetes，大盘给出两种调整方案。", "Use comes from Prometheus, Pod settings and replicas from Kubernetes; the dashboard gives two ways to adjust."),
    "pod-calc": ("单 Pod Requests 与 Limits 的算法", "How the Requests and Limits of a Pod are worked out", "Requests 由持续使用量除以目标占比得到，Limits 由原始 P99 乘以系数得到。", "Requests = sustained use / target share; Limits = raw P99 x coefficient."),
    "replica-calc": ("推荐副本数的算法", "How the recommended replicas are worked out", "CPU 和内存各自算需要的副本数，取较大者，再经过边界与保护。", "CPU and memory each ask for a count; the larger is taken, then bounds and protections."),
    "load-profile": ("常态与高负载时段（示意）", "The normal and the busy hours (illustration)", "按小时看副本数需要和最近 24 小时的实际负载；图中数字是示意。", "Replicas by the hour and the use of the last 24 hours; the figures are illustrative."),
    "strategy-order": ("多个策略的合并顺序", "The order strategies combine in", "旁路与固定值最优先，边界与节奏永远在最后。", "Bypass and fixed values come first; bounds and pace always come last."),
    "module-map": ("模块配置与它控制的内容", "Module settings and what they control", "模块编辑页的五组配置各自决定大盘与执行里的哪一部分。", "Each of the five groups of the module form decides one part of the dashboard and of execution."),
}


class Drawing:
    def __init__(self, kind, lang, height):
        self.kind, self.lang, self.height = kind, lang, height
        self.prefix = f"architecture-{kind}-{lang}"
        self.edges, self.nodes = [], []

    def choose(self, zh, en):
        return zh if self.lang == "zh" else en

    def text(self, x, y, zh, en, small=False, anchor="middle"):
        self.nodes.append(f'<text class="dg-{"note" if small else "title"}" x="{x}" y="{y}" text-anchor="{anchor}">{escape(self.choose(zh, en))}</text>')

    def box(self, x, y, w, h, zh, en, sub_zh="", sub_en="", style="core"):
        label = self.choose(zh, en)
        sub = self.choose(sub_zh, sub_en)
        mid = y + h / 2
        self.nodes.append(f'<g class="dg-box dg-{style}"><rect x="{x}" y="{y}" width="{w}" height="{h}" rx="10"/><text class="dg-title" x="{x+w/2}" y="{mid-5 if sub else mid+6}" text-anchor="middle">{escape(label)}</text>' + (f'<text class="dg-note" x="{x+w/2}" y="{mid+20}" text-anchor="middle">{escape(sub)}</text>' if sub else '') + '</g>')

    def group(self, x, y, w, h, zh, en):
        self.nodes.append(f'<rect class="dg-group" x="{x}" y="{y}" width="{w}" height="{h}" rx="15"/>')
        self.text(x+20, y+29, zh, en, anchor="start")

    def edge(self, points, style="data"):
        self.edges.append(f'<polyline class="dg-edge dg-{style}" points="{" ".join(f"{x},{y}" for x,y in points)}" marker-end="url(#{self.prefix}-{style})"/>')

    def render(self):
        heading = DIAGRAMS[self.kind][0 if self.lang == "zh" else 1]
        description = DIAGRAMS[self.kind][2 if self.lang == "zh" else 3]
        markers = ''.join(f'<marker id="{self.prefix}-{name}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0 10 5 0 10Z" fill="var(--dg-{name})"/></marker>' for name in ("data", "write", "optional"))
        # All selectors are scoped so embedding multiple diagrams cannot change the site's styles.
        css = '''.architecture-graphic{--dg-bg:#fbfdff;--dg-panel:#f1f5fb;--dg-ink:#172c4a;--dg-muted:#536781;--dg-core:#e5efff;--dg-border:#87a9de;--dg-data:#416db1;--dg-write:#197d79;--dg-optional:#8762a9;--dg-exec:#e1f5f0;--dg-store:#fff0d5;--dg-service:#f1eafa;font-family:Inter,"Segoe UI","Microsoft YaHei",sans-serif}
@media(prefers-color-scheme:dark){.architecture-graphic{--dg-bg:#101622;--dg-panel:#172336;--dg-ink:#e5ebf5;--dg-muted:#a8bad2;--dg-core:#203957;--dg-border:#5c7fab;--dg-data:#8fb7ff;--dg-write:#6dd1c2;--dg-optional:#c2a1e1;--dg-exec:#173e3d;--dg-store:#413725;--dg-service:#352b45}}
.architecture-graphic .dg-background{fill:var(--dg-bg)}.architecture-graphic .dg-group{fill:var(--dg-panel);stroke:var(--dg-border);stroke-width:1.5}.architecture-graphic .dg-box rect{fill:var(--dg-core);stroke:var(--dg-border);stroke-width:1.8}.architecture-graphic .dg-exec rect{fill:var(--dg-exec);stroke:var(--dg-write)}.architecture-graphic .dg-store rect{fill:var(--dg-store)}.architecture-graphic .dg-optional rect{fill:var(--dg-service);stroke:var(--dg-optional);stroke-dasharray:6 4}.architecture-graphic .dg-title{font-size:18px;font-weight:650;fill:var(--dg-ink)}.architecture-graphic .dg-note{font-size:14px;font-weight:450;fill:var(--dg-muted)}.architecture-graphic .dg-edge{fill:none;stroke-width:2.2;stroke-linejoin:round;stroke-linecap:round}.architecture-graphic .dg-data{stroke:var(--dg-data)}.architecture-graphic .dg-write{stroke:var(--dg-write)}.architecture-graphic .dg-edge.dg-optional{stroke:var(--dg-optional);stroke-dasharray:6 5}.architecture-graphic{--dg-normal:#4f8fe8;--dg-high:#ef7a2d}.architecture-graphic .dg-cell{stroke:none}.architecture-graphic .dg-bar{fill:var(--dg-normal);opacity:.55}.architecture-graphic .dg-bar-high{fill:var(--dg-high);opacity:.9}.architecture-graphic .dg-outline{fill:none;stroke:var(--dg-high);stroke-width:2}.architecture-graphic .dg-tick{font-size:12px;fill:var(--dg-muted)}'''
        return f'<svg xmlns="http://www.w3.org/2000/svg" class="architecture-graphic" width="1080" height="{self.height}" viewBox="0 0 1080 {self.height}" role="img" aria-labelledby="{self.prefix}-title {self.prefix}-description"><title id="{self.prefix}-title">{escape(heading)}</title><desc id="{self.prefix}-description">{escape(description)}</desc><style>{css}</style><defs>{markers}</defs><rect class="dg-background" width="1080" height="{self.height}" rx="12"/>{"".join(self._layers())}</svg>'

    def _layers(self):
        # Group backgrounds go behind connectors; node boxes and labels go above them.
        groups = [part for part in self.nodes if part.startswith('<rect class="dg-group"')]
        content = [part for part in self.nodes if part not in groups]
        return groups + self.edges + content


def svg(kind, lang):
    if kind == "overview":
        d = Drawing(kind, lang, 940)
        d.group(250,150,550,450,"Platform · 资源预测平台","Platform · forecasting and decisions")
        d.box(300,20,430,80,"管理控制台","Management console","配置 · 推荐 · 效果报告","Settings · advice · effect reports")
        d.box(800,20,250,80,"AI 辅助分析","AI analysis","查询 · Skill · 定时报告","Queries · skills · scheduled reports", "optional")
        d.box(20,215,200,100,"Prometheus","Prometheus","历史与实时指标","History and current metrics")
        d.box(20,80,200,80,"Grafana","Grafana","指标可视化（可选）","Optional visualization", "optional")
        d.box(325,215,400,65,"指标采集与质量","Collection and data quality","查询 · 单位 · 栅格 · 缺样","Queries · units · grids · gaps")
        d.box(325,315,400,65,"预测与评估","Forecasting and evaluation","内置算法 · 回测 · 评分","Methods · backtests · scores")
        d.box(325,415,400,65,"决策与保护","Decisions and safeguards","前瞻 · 共识 · 边界 · 兜底","Lead time · consensus · bounds · fallback")
        d.box(325,515,400,65,"推荐快照","Recommendation snapshot","资源 · 副本 · 版本 · 有效期","Resources · replicas · version · expiry")
        d.box(850,315,200,75,"时序模型服务","Time-series service","可选外部预测","Optional model inference", "optional")
        d.box(850,470,200,85,"Operator","Operator","Scale API · 写入副本","Scale API · replicas", "exec")
        d.box(850,625,200,85,"Kyverno 准入","Kyverno admission","新 Pod 资源配置","Resources for new Pods", "exec")
        d.box(325,665,400,85,"关系数据库","Relational database","SQLite / MySQL / PostgreSQL · 任选一种","SQLite / MySQL / PostgreSQL · choose one", "store")
        d.box(850,770,200,75,"Kubernetes","Kubernetes","工作负载与 Pod","Workloads and Pods", "exec")
        d.edge([(515,100),(515,150)])
        d.edge([(925,100),(925,125),(700,125),(700,150)],"optional")
        d.edge([(220,265),(275,265),(275,247),(325,247)])
        d.edge([(120,215),(120,160)])
        for y in (280,380,480):d.edge([(525,y),(525,y+35)])
        d.edge([(725,347),(850,352)],"optional")
        d.edge([(725,547),(820,547),(820,512),(850,512)])
        d.edge([(725,565),(780,565),(780,667),(850,667)])
        d.edge([(525,600),(525,665)])
        d.text(535,638,"持久化记录","Persistent records",True,anchor="start")
        d.edge([(1050,512),(1068,512),(1068,807),(1050,807)],"write")
        d.edge([(950,710),(950,770)],"write")
        d.text(960,740,"Pod 配置","Pod settings",True,anchor="start")
        d.edge([(950,845),(950,870),(120,870),(120,315)])
        d.text(460,856,"实测指标与 Ready 状态回流","Observed metrics and Ready state feed back",True)
        d.text(540,912,"蓝：数据关系   绿：集群写入   虚线：可选能力","Blue: data   Green: cluster writes   Dashed: optional",True)
    elif kind == "modules":
        d=Drawing(kind,lang,660)
        d.box(40,20,1000,80,"管理控制台","Management console","模块 · 数据源 · 算法 · AI 配置 · 审计","Modules · sources · methods · AI configuration · audit")
        d.group(40,155,1000,325,"Platform · 共享业务与权限","Platform · shared services and access control")
        d.box(75,225,270,85,"模块与指标配置","Modules and metrics","工作负载 · 历史 · 周期 · 边界","Workload · history · schedule · bounds")
        d.box(405,225,270,85,"预测与效果评估","Forecasting and evaluation","运行 · 回测 · 回填 · 排名","Runs · backtests · actuals · rankings")
        d.box(735,225,270,85,"推荐与执行接口","Advice and execution","快照 · 准入查询 · 副本决策","Snapshots · admission · replicas")
        d.box(75,355,270,85,"任务中心","Task center","计划 · 作业 · 重试 · 清理","Schedules · jobs · retries · retention")
        d.box(405,355,270,85,"AI 助手与报告","AI assistant and reports","工具查询 · Skill · 邮件任务","Data tools · skills · email tasks")
        d.box(735,355,270,85,"系统与审计","Administration and audit","用户 · LDAP · 权限 · 操作记录","Users · LDAP · roles · action records")
        d.box(40,545,470,80,"共享关系数据库","Shared relational database","配置 · 历史 · 任务 · 推荐 · 审计","Settings · history · jobs · advice · audit", "store")
        d.box(570,545,470,80,"可选模型连接","Optional model connections","时序模型用于预测 · LLM 用于分析","Time-series models forecast · LLMs analyze", "optional")
        d.edge([(540,100),(540,155)])
        d.edge([(345,267),(405,267)])
        d.edge([(675,267),(735,267)])
        d.edge([(210,355),(210,330),(540,330),(540,310)])
        d.edge([(275,480),(275,545)])
        d.edge([(540,440),(540,510),(805,510),(805,545)],"optional")
    elif kind == "execution":
        d=Drawing(kind,lang,735)
        d.box(410,20,260,85,"已验证的推荐快照","Validated snapshot","新鲜度 · 版本 · 授权","Freshness · version · authorization")
        d.group(20,170,495,355,"路径一 · 新 Pod 资源","Path 1 · resources of new Pods")
        d.group(565,170,495,355,"路径二 · 工作负载副本","Path 2 · workload replicas")
        d.box(90,235,340,65,"创建 Pod 的准入请求","Pod creation admission request","Kubernetes API Server","Kubernetes API Server", "exec")
        d.box(90,340,340,65,"Kyverno 查询资源建议","Kyverno queries resource advice","只读取快照，不运行模型","Snapshot only, no model inference")
        d.box(90,445,340,65,"应用 requests / limits","Apply requests / limits","按模块授权与策略处理","Subject to module policy", "exec")
        d.box(645,235,340,65,"Operator 读取模块和决策","Operator reads advice","平台提供目标副本数","Platform provides target replicas")
        d.box(645,340,340,65,"执行规则检查","Check execution rules","边界 · 就绪 · 冷却 · 步长","Bounds · readiness · cooldown · step")
        d.box(645,445,340,65,"Scale API 写入 replicas","Write replicas through Scale API","唯一副本控制者","One replica controller", "exec")
        d.box(410,600,260,75,"Kubernetes 实际结果","Kubernetes outcomes","Pod 资源与实际就绪容量","Pod resources and Ready capacity", "exec")
        d.edge([(410,62),(35,62),(35,372),(90,372)])
        d.edge([(670,62),(1045,62),(1045,267),(985,267)])
        for x in (260,815):
            d.edge([(x,300),(x,340)])
            d.edge([(x,405),(x,445)])
        d.edge([(260,510),(260,637),(410,637)],"write")
        d.edge([(815,510),(815,637),(670,637)],"write")
        d.text(260,148,"失效时保留原始资源","Invalid advice preserves resources",True)
        d.text(815,148,"失联按缓存与恢复规则处理","Outages use cache and recovery rules",True)
        d.text(540,713,"资源准入和副本执行分别授权，不交叉写入","Authorize admission and scaling separately; no overlapping writes",True)
    elif kind == "cycles":
        d=Drawing(kind,lang,700)
        d.group(20,20,1040,190,"① 预测周期 · 按模块间隔运行","1 · Forecast cycle · module schedule")
        for x,zh,en,subzh,suben in [(55,"采集","Collect","最新与历史指标","History + current"),(310,"预测","Forecast","所选算法与模型","Methods + models"),(565,"形成建议","Recommend","共识与保护","Consensus and safeguards"),(820,"保存快照","Save snapshot","来源与有效期","Sources and expiry")]:
            d.box(x,95,205,75,zh,en,subzh,suben)
        for x in (260,515,770):d.edge([(x,132),(x+50,132)])
        d.group(20,250,1040,190,"② 评估周期 · 实际值到达后","2 · Evaluation cycle · after actuals arrive")
        for x,zh,en,subzh,suben in [(55,"实测回填","Match actuals","对齐预测时间","Align forecast target times"),(310,"计算误差","Measure error","低估与覆盖率","Underestimate · coverage"),(565,"评分与排名","Score and rank","比较候选表现","Compare candidate quality"),(820,"下一轮决策","Next decision","采用资格与可信度","Eligibility and credibility")]:
            d.box(x,325,205,75,zh,en,subzh,suben)
        for x in (260,515,770):d.edge([(x,362),(x+50,362)])
        d.group(20,480,1040,190,"③ 决策刷新 · 两次预测之间跟随最新负载","3 · Decision refresh · follow load between forecasts")
        for x,zh,en,subzh,suben in [(55,"Operator 轮询","Operator polls","读取最新决策","Read latest decision"),(310,"更新实测样本","Refresh observations","预测仍新则复用","Reuse fresh predictions"),(565,"重新计算决策","Recompute advice","新负载与原预测","Load + saved forecasts"),(820,"Operator 执行","Operator applies","再检查执行限制","Check execution rules again")]:
            d.box(x,555,205,75,zh,en,subzh,suben)
        for x in (260,515,770):d.edge([(x,592),(x+50,592)])
    elif kind == "dashboard-flow":
        d = Drawing(kind, lang, 700)
        d.box(20, 70, 230, 85, "Prometheus", "Prometheus", "使用量 · Requests · Limits", "Use · Requests · Limits")
        d.box(20, 235, 230, 100, "Kubernetes", "Kubernetes", "Operator 只读上报", "Read-only report of the Operator", "exec")
        d.text(135, 365, "Pod 模板 · 副本数 · Pod 列表", "Pod template · replicas · Pods", True)
        d.group(300, 30, 480, 570, "模块资源分析 · 只读", "Module analysis · read only")
        d.box(330, 85, 420, 80, "百分位与持续使用量", "Percentiles and sustained use", "P90 · P95 · P99，平台对原始点计算", "P90 · P95 · P99 worked out by the platform")
        d.box(330, 195, 420, 80, "② 调整单 Pod 配置", "2 · Change the Pod", "副本数不变：Requests · Limits", "Replicas stay: Requests · Limits")
        d.box(330, 305, 420, 80, "① 调整副本数", "1 · Change the replicas", "Pod 配置不变：CPU · 内存取较大者", "The Pod stays: the larger of CPU and memory")
        d.box(330, 415, 420, 80, "常态与高负载时段", "Normal and busy hours", "24 小时 · 副本数 · 实际负载", "24 hours · replicas · actual load")
        d.box(330, 520, 420, 60, "计算依据", "Basis", "来源 · 单位 · 系数 · 取整", "Sources · units · coefficients · rounding")
        d.box(830, 70, 220, 85, "模块配置", "Module settings", "系数 · 边界 · 策略", "Coefficients · bounds · strategies")
        d.box(830, 235, 220, 85, "AI 分析与邮件", "AI analysis and mail", "点按钮才开始", "Starts on the button only", "optional")
        d.box(830, 400, 220, 85, "Operator", "Operator", "Auto 模式才写副本", "Writes replicas in Auto only", "exec")
        d.edge([(250, 112), (290, 112), (290, 125), (330, 125)])
        d.edge([(250, 285), (290, 285), (290, 235), (330, 235)])
        d.edge([(290, 285), (290, 345), (330, 345)])
        d.edge([(830, 112), (790, 112), (790, 125), (750, 125)])
        d.edge([(750, 235), (790, 235), (790, 277), (830, 277)], "optional")
        d.edge([(750, 345), (800, 345), (800, 442), (830, 442)], "write")
        d.text(540, 650, "蓝：数据关系   绿：集群写入   虚线：可选能力", "Blue: data   Green: cluster writes   Dashed: optional", True)
    elif kind == "pod-calc":
        d = Drawing(kind, lang, 700)
        d.group(30, 20, 500, 640, "Requests · 持续使用量", "Requests · sustained use")
        d.group(560, 20, 490, 640, "Limits · 原始峰值", "Limits · raw peak")
        d.box(60, 70, 440, 70, "P90 · P95 · P99 持续使用量", "Sustained P90 · P95 · P99", "单 Pod，每点取前 30 分钟均值", "One Pod; each point the mean of the last 30 minutes")
        d.box(60, 180, 440, 70, "各除以自己的目标占比", "Each divided by its target share", "默认 80% · 85% · 110%", "Default 80% · 85% · 110%")
        d.box(60, 290, 440, 60, "取最大的一个", "Take the largest")
        d.box(60, 390, 440, 60, "× (1 + Requests 余量)", "x (1 + Requests margin)", "可选，默认不加", "Optional, none by default")
        d.box(60, 490, 440, 70, "按规格向上取整", "Round up to the spec step", "CPU 50 mCPU · 内存 64 MiB", "CPU 50 mCPU · memory 64 MiB")
        d.box(60, 590, 440, 55, "推荐 Requests", "Recommended Requests", style="exec")
        d.edge([(280, 140), (280, 180)])
        d.edge([(280, 250), (280, 290)])
        d.edge([(280, 350), (280, 390)])
        d.edge([(280, 450), (280, 490)])
        d.edge([(280, 560), (280, 590)])
        d.box(590, 70, 430, 70, "原始 P99", "Raw P99", "单 Pod，不做持续处理", "One Pod; not held")
        d.box(590, 180, 430, 70, "× Limits 系数", "x Limits coefficient", "默认 120%（即 P99 × 1.2）", "Default 120% (P99 x 1.2)")
        d.box(590, 290, 430, 60, "max(推荐 Requests, 上一步)", "max(recommended Requests, previous)")
        d.box(590, 590, 430, 55, "推荐 Limits", "Recommended Limits", style="exec")
        d.edge([(805, 140), (805, 180)])
        d.edge([(805, 250), (805, 290)])
        d.edge([(805, 350), (805, 590)])
        d.edge([(500, 617), (530, 617), (530, 320), (590, 320)], "write")
        d.text(830, 440, "Limits 不低于 Requests", "The Limits is never below the Requests", True, anchor="start")
        d.text(830, 470, "例：5.25 GiB × 120% = 6.3 GiB", "e.g. 5.25 GiB x 120% = 6.3 GiB", True, anchor="start")
        d.text(830, 498, "max(6.06, 6.3) = 6.3 GiB", "max(6.06, 6.3) = 6.3 GiB", True, anchor="start")
        d.text(280, 682, "例：5.11 GiB ÷ 85% ≈ 6.01 GiB → 6.06 GiB", "e.g. 5.11 GiB / 85% = 6.01 GiB -> 6.06 GiB", True)
    elif kind == "replica-calc":
        d = Drawing(kind, lang, 750)
        d.box(40, 20, 440, 70, "全副本使用量", "Use of all the replicas", "P90 · P95 · P99 持续使用量", "Sustained P90 · P95 · P99")
        d.box(600, 20, 440, 70, "单 Pod Requests 与目标占比", "Requests of a Pod and target share", "Pod 模板来自 Kubernetes", "The Pod template from Kubernetes", "exec")
        d.box(300, 130, 480, 70, "每个百分位：总量 ÷ (Requests × 占比)", "Each percentile: total / (Requests x share)")
        d.box(40, 245, 300, 70, "CPU 需要", "CPU asks for", "三个百分位取最大，向上取整", "Largest of three, rounded up")
        d.box(390, 245, 300, 70, "内存 需要", "Memory asks for", "同上", "The same")
        d.box(740, 245, 300, 70, "Limits 保护", "Limits guard", "P99 总量 ÷ (Limits × 90%)", "P99 total / (Limits x 90%)")
        d.box(300, 360, 480, 65, "取较大者，至少 1 个", "The larger, at least 1")
        d.box(300, 465, 480, 65, "模块的最小 / 最大副本数", "Least and most replicas of the module")
        d.box(830, 465, 210, 65, "OOM 或限流", "OOM or throttling", "不减副本", "No scale-in", "optional")
        d.box(300, 570, 480, 75, "推荐副本数", "Recommended replicas", "不含调整节奏", "Pace not included")
        d.box(830, 570, 210, 75, "本次动作到", "This action reaches", "单次最多 N 个", "At most N at once", "exec")
        d.edge([(260, 90), (260, 110), (450, 110), (450, 130)])
        d.edge([(820, 90), (820, 110), (630, 110), (630, 130)])
        d.edge([(540, 200), (540, 222), (190, 222), (190, 245)])
        d.edge([(540, 200), (540, 245)])
        d.edge([(540, 222), (890, 222), (890, 245)])
        d.edge([(190, 315), (190, 338), (540, 338), (540, 360)])
        d.edge([(540, 315), (540, 360)])
        d.edge([(890, 315), (890, 338), (540, 338)])
        d.edge([(540, 425), (540, 465)])
        d.edge([(540, 530), (540, 570)])
        d.edge([(830, 497), (780, 497)], "optional")
        d.edge([(780, 607), (830, 607)], "write")
        d.text(540, 692, "例：内存 60.71 GiB ÷ (6 GiB × 85%) = 11.9 → 12", "e.g. memory 60.71 GiB / (6 GiB x 85%) = 11.9 -> 12", True)
        d.text(540, 720, "CPU 单独算只需 9 到 11 个时，内存仍决定全天的 12 个", "When CPU alone needs 9 to 11, memory still sets 12", True)
    elif kind == "load-profile":
        d = Drawing(kind, lang, 560)
        d.text(40, 40, "副本数时段分布 · 每小时需要的副本数", "Replicas by the hour · replicas each hour asks for", anchor="start")
        need = [2] * 24
        for h in (9, 10, 14, 15, 16):
            need[h] = 6
        need[11] = 4
        need[13] = 4
        for h, v in enumerate(need):
            x = 60 + h * 40
            height = v * 24
            cls = "dg-bar-high" if v > 2 else "dg-bar"
            d.nodes.append(f'<rect class="{cls}" x="{x}" y="{230-height}" width="30" height="{height}" rx="3"/>')
            if h % 3 == 0:
                d.nodes.append(f'<text class="dg-tick" x="{x+15}" y="250" text-anchor="middle">{h:02d}</text>')
        d.text(60, 275, "常态 2 个，最忙 6 个，高峰时段 09–12、13–17", "Normal 2, busiest 6; busy hours 09-12 and 13-17", True, anchor="start")
        d.nodes.append('<line x1="40" y1="292" x2="1040" y2="292" stroke="var(--dg-border)" stroke-dasharray="4 4"/>')
        d.text(40, 325, "实际资源负载时段分布 · 最近 24 小时，每格 10 分钟", "The use itself · last 24 hours, 10 minutes a cell", anchor="start")

        def row(y, name_zh, name_en, high):
            d.text(60, y - 8, name_zh, name_en, True, anchor="start")
            for h in range(24):
                x = 60 + h * 40
                hot = h in high
                d.nodes.append(f'<rect class="dg-outline" x="{x-2}" y="{y-1}" width="34" height="32" rx="3"' + ('' if hot else ' style="stroke:none"') + '/>')
                for c in range(6):
                    if hot:
                        fill = f"rgba(239,122,45,{0.55 + 0.07 * ((h + c) % 5):.2f})"
                    else:
                        fill = f"rgba(79,143,232,{0.18 + 0.05 * ((h * 3 + c) % 4):.2f})"
                    d.nodes.append(f'<rect class="dg-cell" x="{x+c*5}" y="{y+2}" width="4" height="26" rx="1" fill="{fill}"/>')
        row(370, "CPU", "CPU", {9, 10, 14, 15, 16})
        row(430, "内存", "Memory", set())
        for h in range(0, 24, 3):
            d.nodes.append(f'<text class="dg-tick" x="{60+h*40+15}" y="478" text-anchor="middle">{h:02d}</text>')
        d.nodes.append('<rect class="dg-bar" x="60" y="500" width="14" height="14" rx="2"/>')
        d.text(82, 512, "常态负载（不超过常态副本数能承载的量）", "Normal (within what the normal replicas carry)", True, anchor="start")
        d.nodes.append('<rect class="dg-bar-high" x="470" y="500" width="14" height="14" rx="2"/>')
        d.text(492, 512, "高负载（超过它）", "High (above it)", True, anchor="start")
        d.text(700, 512, "数字为示意，不是实测", "The figures are illustrative, not measured", True, anchor="start")
    elif kind == "strategy-order":
        d = Drawing(kind, lang, 770)
        steps = [
            ("旁路", "Bypass", "人工接管，模块不参与决策", "Manual takeover; the module takes no part", True),
            ("时间周期里的“固定”", "A period that fixes the replicas", "这段时间副本数就是这个数", "The count is that number for the period", True),
            ("固定副本数", "Fixed replicas", "写死的常数，预测与触发不改它", "A constant that forecasts and triggers do not change", True),
            ("预测（或降级默认值）", "Forecast (or the fallback)", "副本数的基础值", "The base value of the replicas", False),
            ("指标触发", "Metric triggers", "设定 > 扩容 > 缩容", "Set > scale out > scale in", False),
            ("时间周期里的“至少 / 至多”", "A period with at least / at most", "抬高或压低", "Raise or cap", False),
            ("副本边界", "Bounds", "最小 / 最大副本数，永远在最后", "Least and most replicas, always last", False),
            ("调整节奏", "Pace", "单次步长与冷却时间", "Step and cooldown of one action", False),
        ]
        for i, (zh, en, sub_zh, sub_en, first) in enumerate(steps):
            y = 20 + i * 90
            d.box(40, y, 420, 64, zh, en, style="exec" if first else "core")
            d.text(490, y + 38, sub_zh, sub_en, True, anchor="start")
            if i < len(steps) - 1:
                d.edge([(250, y + 64), (250, y + 90)])
        d.text(540, 755, "绿色三步是管理员写定的值：之后的策略不改它们", "The green steps are values the administrator fixed; later strategies do not change them", True)
    elif kind == "module-map":
        d = Drawing(kind, lang, 640)
        d.group(20, 30, 340, 570, "模块编辑页", "Module form")
        d.group(780, 30, 280, 570, "结果与执行", "Results and execution")
        rows = [
            ("基本与指标来源", "Workload and sources", "工作负载 · 数据源 · 指标模板", "Workload · data sources · templates", "读什么数据", "What data is read", "使用量 · Requests · Limits 的来源", "Where use, Requests and Limits come from"),
            ("计算参数", "Calculation", "容量 · 推荐系数 · OOM · JVM", "Capacity · coefficients · OOM · JVM", "怎么算", "How it is worked out", "Requests · Limits · 副本数的算法", "Requests · Limits · replica rules"),
            ("安全边界", "Safety bounds", "副本上下限 · 节奏 · 降级值", "Replica bounds · pace · fallback", "不能越过的范围", "What may not be crossed", "推荐值最终要落在的区间", "The range a result must end in"),
            ("模块策略", "Strategies", "预测 · 时间周期 · 触发 · 固定", "Forecast · periods · triggers · fixed", "什么时候用什么", "When to use which", "副本数在不同时刻的来源", "Where the replicas come from at each moment"),
            ("运行模式", "Mode", "Observe · Recommend · Admission · Auto", "Observe · Recommend · Admission · Auto", "允许做什么", "What is allowed", "只看、推荐、准入还是自动", "Watch, advise, admit or act"),
        ]
        for i, (az, ae, asz, ase, bz, be, bsz, bse) in enumerate(rows):
            y = 80 + i * 100
            d.box(40, y, 300, 76, az, ae, asz, ase)
            d.box(410, y, 320, 76, bz, be, bsz, bse, "exec" if i == 4 else "core")
            d.edge([(340, y + 38), (410, y + 38)])
            d.edge([(730, y + 38), (780, y + 38)])
        d.text(920, 330, "资源大盘", "Resource dashboard", False)
        d.text(920, 365, "推荐快照", "Recommendation snapshot", False)
        d.text(920, 400, "准入 · Operator", "Admission · Operator", False)
        d.text(540, 628, "每一项配置都能在模块列表的“参数”窗口里按分组查看生效值", "Every setting can be read by group in the Parameters window of the module list", True)
    else:
        raise ValueError(kind)
    return d.render()


def figure(kind, lang, source):
    title=DIAGRAMS[kind][0 if lang=="zh" else 1]
    label=lambda zh,en: zh if lang=="zh" else en
    controls=''.join(f'<button type="button" data-diagram-action="{action}" aria-label="{label(zh,en)}">{label(zh,en)}</button>' for action,zh,en in [('out','缩小','Zoom out'),('in','放大','Zoom in'),('fit','适应宽度','Fit width'),('fullscreen','全屏','Full screen')])
    return f'<figure class="architecture-figure" data-diagram><div class="diagram-toolbar"><strong>{title}</strong><div class="diagram-controls">{controls}<output aria-live="polite">100%</output><a href="{source}" target="_blank" rel="noopener">{label("打开 SVG ↗","Open SVG ↗")}</a></div></div><div class="diagram-viewport" tabindex="0" aria-label="{label("架构图，放大后可滚动查看","Architecture diagram, scroll after zooming")}"><div class="diagram-canvas">{svg(kind,lang)}</div></div><figcaption>{label("箭头表示数据或控制关系；放大后可滚动，原图可保存。","Arrows represent data or control relationships. Zoom and scroll, or save the original SVG.")}</figcaption></figure>'
