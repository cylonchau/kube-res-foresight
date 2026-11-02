# kube-res-foresight 名称与升级

项目统一使用 `kube-res-foresight`。Go 模块为 `github.com/cylonchau/kube-res-foresight`，程序、镜像示例、Helm Chart、前端和官网同步采用新名称。

| 用途 | 当前标识 |
|---|---|
| 命令行 | `kube-res-foresight` / `kube-res-foresightctl` |
| 环境变量 | `KUBE_RES_FORESIGHT_*` |
| 凭据加密密钥 | `KUBE_RES_FORESIGHT_SECRET_KEY` |
| API 客户端头 | `X-kube-res-foresight-Client` |
| 开发者凭据头 | `X-kube-res-foresight-AccessKey` / `X-kube-res-foresight-AccessSecret` |
| Authorization scheme | `kube-res-foresight-AK <key>:<secret>` |
| WebSocket 子协议 | `kube-res-foresight.jwt` |
| 指标 | `kube_res_foresight_*` / `kube_res_foresight_operator_*` |
| 新安装 SQLite 默认文件 | `kube-res-foresight.db` |

## 配置、数据库与密钥

- 新环境变量优先，其次读取 `KUBE_RESOURCE_FCST_*`，最后读取更早的 `KUBEFORECAST_*`。显式设置的新变量（包括空值）优先，不会被旧值覆盖。时区变量也保留这两个旧前缀的兼容读取。
- 只调整变量名称，保持已有 JWT 密钥、凭据加密密钥、数据库连接信息的值不变。既有凭据无需重新加密，数据库里的业务记录不批量改名。
- 配置使用新默认 SQLite 文件，而文件不存在时，优先使用同目录的 `kube-resource-fcst.db`，再寻找 `kubeforecast.db`。新文件已存在或使用自定义路径时遵循配置。
- 不移动运行中的 SQLite 主文件、WAL 或 SHM。需要更改物理文件名时先停服务并备份，再通过安全备份/恢复流程处理。

## 客户端、监控与部署

平台**同时认**前两代名称，所以升级过程中还在跑的旧 Operator、已部署的 Kyverno 策略、脚本、浏览器里没刷新的旧页面都能继续用；发出去的请求只用新名称。

| 项目 | 现在的名称 | 平台仍接受 |
|---|---|---|
| 开发者凭据头 | `X-kube-res-foresight-AccessKey` / `-AccessSecret` | `X-kube-resource-fcst-…`、`X-KubeForecast-…` |
| Authorization scheme | `kube-res-foresight-AK <key>:<secret>` | `kube-resource-fcst-AK`、`KubeForecast-AK`（不分大小写） |
| 查询参数 | `__kube_res_foresight_ak` / `_sk` | `__kube_resource_fcst_ak/sk`、`__kubeforecast_ak/sk` |
| WebSocket 子协议 | `kube-res-foresight.jwt` | `kube-resource-fcst.jwt`、`kubeforecast.jwt`（应答用客户端提供的那个） |
| 客户端头 | `X-kube-res-foresight-Client` / `-Client-Name` | 前两代同名头 |
| Operator 选主租约 | `kube-resource-fcst-operator.autoscaling.kube-resource-fcst.io` | **仍用旧名**（见下） |

已经签发的开发者凭据（`ak_kube_resource_fcst_…`）不用重发：平台按库里的记录校验，不看前缀。

**Operator 的选主租约故意保持旧名。** 租约名随改名而变会让新旧两个 Operator 同时当选，同时改同一批模块的副本数。保持不变后，新 Operator 会等旧的让出租约（滚动替换时正好互斥）。只有在集群里确认没有旧版本 Operator 之后才可以改这个名字。

**指标前缀**：新前缀是 `kube_res_foresight_*`。生产里的告警和看板是用旧前缀写的，改名后它们会不报错地失效。过渡期把 `metrics.legacy_names: true`（环境变量 `KUBE_RES_FORESIGHT_METRICS_LEGACY_NAMES=true`，Helm `metrics.legacyNames=true`）打开，平台的 `/metrics` 会同时导出两套名称，值相同；告警和看板迁完后关掉。Operator 的指标（`kube_res_foresight_operator_*`）没有这个开关，Prometheus 规则里用 `{__name__=~"kube_(res_foresight|resource_fcst)_operator_.*"}` 同时匹配两代，直到旧 Operator 下线。历史窗口里仍是旧前缀。

### 升级顺序（生产）

1. 备份数据库，保留现有的 `KUBE_RESOURCE_FCST_SERVER_JWT_SECRET` 和 `KUBE_RESOURCE_FCST_SECRET_KEY` 的**值**（变量名可以继续用旧的，也可以换成 `KUBE_RES_FORESIGHT_*`，值不能变，否则登录会话失效、已加密的密钥读不出来）。
2. 先升级平台，再升级 Operator。新平台认旧 Operator；旧平台不认新 Operator 的请求头，反过来不行。
3. 打开 `metrics.legacy_names`，迁移告警和看板，再关掉。
4. 把 Kyverno 策略、脚本换成新的头名称（旧的也还能用，但不要再新增）。
5. 所有旧 Operator 下线后，才考虑更改租约名。

### 已验证的升级

用改名前的版本（提交 `bef42db`）建库、签发 Operator 凭据、保存加密的 LLM 密钥，再用新版本在**同一个数据库文件、旧环境变量**下启动：旧登录令牌仍有效；同一管理员密码可登录；旧凭据用三代请求头和三代 Authorization scheme 都通过，错误密钥仍被拒、无权限的接口仍被拒；加密保存的 LLM 密钥仍能解密使用；没有新建空库。没有验证：MySQL/PostgreSQL 上的升级、真实集群里新旧 Operator 同时存在（租约名保持一致是按代码和单元测试保证的，没有在集群里并排跑两个版本）。

当前 Operator 只读取平台模块并修改目标工作负载副本，不使用 PredictiveScaler CRD。升级时保留平台地址、凭据与工作负载配置，确认新旧 Operator 不会因 Deployment 或 Lease 改名而同时控制同一目标，再恢复自动执行。不自动更改集群中的 Namespace、Secret、RBAC 或 Helm release。

Operator Helm 路径为 `deploy/helm/kube-res-foresight-operator`。平台 Chart 如在本地维护，路径同步为 `deploy/helm/kube-res-foresight`。管理前端位于独立的 `web/` Git 仓库，分别检查与提交。

## GitHub 仓库与 Pages

本地名称调整不会自动改变 GitHub 的仓库名。当前远端仍为 `cylonchau/hermes`，官网仓库链接仍指向这个实际存在的地址。本次环境未提供可用于重命名 GitHub 仓库的管理凭据。

仓库管理员可在 GitHub 的 Settings → General → Repository name 修改为 `kube-res-foresight`，随后把本地 origin 更新为 `https://github.com/cylonchau/kube-res-foresight.git`，并同步 `site/assets/config.js` 的仓库地址。Pages 使用相对链接，支持新仓库子路径；检查 Settings → Pages 与工作流运行状态。这里不执行推送、远端改名或集群操作。
