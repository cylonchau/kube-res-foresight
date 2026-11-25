"""Original, bilingual SVG architecture figures for the buildless documentation site."""
from html import escape

DIAGRAMS = {
    "overview": ("系统总览", "System overview", "指标、平台、推荐、执行与实测反馈之间的关系。", "Metrics, the platform, advice, execution, and observed feedback."),
    "modules": ("功能模块架构", "Functional modules", "平台内的配置、预测、任务、接口与共享存储。", "Configuration, forecasting, jobs, interfaces, and shared storage."),
    "cycles": ("预测与反馈循环", "Forecast and feedback cycles", "预测周期、实测回填与按需刷新分别运行。", "Forecasting, actual-value evaluation, and on-demand refresh run separately."),
    "execution": ("资源与副本执行路径", "Resource and replica execution", "准入设置新 Pod 资源，Operator 单独控制副本。", "Admission configures new Pods; the Operator separately controls replicas."),
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
.architecture-graphic .dg-background{fill:var(--dg-bg)}.architecture-graphic .dg-group{fill:var(--dg-panel);stroke:var(--dg-border);stroke-width:1.5}.architecture-graphic .dg-box rect{fill:var(--dg-core);stroke:var(--dg-border);stroke-width:1.8}.architecture-graphic .dg-exec rect{fill:var(--dg-exec);stroke:var(--dg-write)}.architecture-graphic .dg-store rect{fill:var(--dg-store)}.architecture-graphic .dg-optional rect{fill:var(--dg-service);stroke:var(--dg-optional);stroke-dasharray:6 4}.architecture-graphic .dg-title{font-size:18px;font-weight:650;fill:var(--dg-ink)}.architecture-graphic .dg-note{font-size:14px;font-weight:450;fill:var(--dg-muted)}.architecture-graphic .dg-edge{fill:none;stroke-width:2.2;stroke-linejoin:round;stroke-linecap:round}.architecture-graphic .dg-data{stroke:var(--dg-data)}.architecture-graphic .dg-write{stroke:var(--dg-write)}.architecture-graphic .dg-edge.dg-optional{stroke:var(--dg-optional);stroke-dasharray:6 5}'''
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
    else:
        raise ValueError(kind)
    return d.render()


def figure(kind, lang, source):
    title=DIAGRAMS[kind][0 if lang=="zh" else 1]
    label=lambda zh,en: zh if lang=="zh" else en
    controls=''.join(f'<button type="button" data-diagram-action="{action}" aria-label="{label(zh,en)}">{label(zh,en)}</button>' for action,zh,en in [('out','缩小','Zoom out'),('in','放大','Zoom in'),('fit','适应宽度','Fit width'),('fullscreen','全屏','Full screen')])
    return f'<figure class="architecture-figure" data-diagram><div class="diagram-toolbar"><strong>{title}</strong><div class="diagram-controls">{controls}<output aria-live="polite">100%</output><a href="{source}" target="_blank" rel="noopener">{label("打开 SVG ↗","Open SVG ↗")}</a></div></div><div class="diagram-viewport" tabindex="0" aria-label="{label("架构图，放大后可滚动查看","Architecture diagram, scroll after zooming")}"><div class="diagram-canvas">{svg(kind,lang)}</div></div><figcaption>{label("箭头表示数据或控制关系；放大后可滚动，原图可保存。","Arrows represent data or control relationships. Zoom and scroll, or save the original SVG.")}</figcaption></figure>'
