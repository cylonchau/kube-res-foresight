# kube-res-foresight

让资源配置，跟上业务节奏。

面向单个 Kubernetes 集群的资源预测与弹性调度平台：连接指标、比较预测、审查建议，让资源调整有据可循。

## 它做什么

围绕「模块」工作。一个模块对应一个工作负载，记录要看的指标、历史窗口、预测周期、副本和资源边界。

1. **接入指标**：从 Prometheus 按模块读取 CPU、内存、请求量等历史，检查缺口、单位和新鲜度。
2. **预测与评估**：多种算法（基线、统计、机器学习、外部时序模型、自定义公式）同时预测，用滚动回测和实际值打分，选出表现最好的。
3. **生成建议**：结合提前量、容量条件、固定周期和 OOM 保护，得出带版本、有有效期的资源规格和目标副本。
4. **受控执行**：新 Pod 的 CPU、内存在准入时按建议设置；副本数由 operator 调整。两条路径分别授权，各有边界。

另有 AI 助手（对话查询、定时提问并邮件发送）、LDAP 登录、用户角色权限、任务中心和操作审计。

## 从观察开始

模块有四种模式，逐级放开，默认什么都不改：

| 模式 | 行为 |
|---|---|
| 仅观察 | 只采集和预测 |
| 仅推荐 | 生成建议供人审查 |
| 准入 | 新建 Pod 时按建议设置 CPU、内存 |
| 自动 | operator 按决策调整副本 |

数据不足或预测失败时，使用模块里配置的降级默认值，恢复要连续几个健康周期，避免来回抖动。平台暂时连不上时，operator 先沿用缓存的决策，不会立刻扩缩。同一个工作负载只能有一个副本控制者，不能再和 HPA/KEDA 一起管。

## 快速开始

需要 Go、Node 22+ 和 pnpm。

```sh
make
```

编译出平台和 operator，控制台已经嵌在平台里。运行：

```sh
target/bin/kube-res-foresight server --config config.yaml --migration
target/bin/kube-res-foresight all --config config.yaml
```

打开控制台，默认账号 `admin` / `123456`，登录后请立即改密码。

构建镜像用 `make docker`，推送用 `make docker-push`，用 `REGISTRY` 和 `IMAGE_TAG` 指定仓库和标签。Kubernetes 部署（Helm）、Operator 凭据、数据库等见[安装与部署文档](website/content/zh/installation.html)。

## 测试

```sh
go test ./...
cd web && pnpm typecheck && pnpm test:unit
```
