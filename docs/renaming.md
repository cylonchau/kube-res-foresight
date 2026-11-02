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

同步更新前后端、Operator、CLI 和外部调用方的请求头、Authorization scheme、WebSocket 子协议。Prometheus 查询、告警和 Grafana 使用新指标前缀；不重复导出旧指标，历史窗口仍需查询原监控存储中的旧前缀。

当前 Operator 只读取平台模块并修改目标工作负载副本，不使用 PredictiveScaler CRD。升级时保留平台地址、凭据与工作负载配置，确认新旧 Operator 不会因 Deployment 或 Lease 改名而同时控制同一目标，再恢复自动执行。不自动更改集群中的 Namespace、Secret、RBAC 或 Helm release。

Operator Helm 路径为 `deploy/helm/kube-res-foresight-operator`。平台 Chart 如在本地维护，路径同步为 `deploy/helm/kube-res-foresight`。管理前端位于独立的 `web/` Git 仓库，分别检查与提交。

## GitHub 仓库与 Pages

本地名称调整不会自动改变 GitHub 的仓库名。当前远端仍为 `cylonchau/hermes`，官网仓库链接仍指向这个实际存在的地址。本次环境未提供可用于重命名 GitHub 仓库的管理凭据。

仓库管理员可在 GitHub 的 Settings → General → Repository name 修改为 `kube-res-foresight`，随后把本地 origin 更新为 `https://github.com/cylonchau/kube-res-foresight.git`，并同步 `site/assets/config.js` 的仓库地址。Pages 使用相对链接，支持新仓库子路径；检查 Settings → Pages 与工作流运行状态。这里不执行推送、远端改名或集群操作。
