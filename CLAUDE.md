# KubeForecast

开发、排查、评审和规划本项目时，使用 [kubeforecast-development](.claude/skills/kubeforecast-development/SKILL.md) 技能。

项目规则见 [AGENTS.md](AGENTS.md)，专项技能在 `.agents/skills/` 与 `.claude/skills/` 双向同步；Claude 入口按任务读取 `.claude/skills/`。执行状态见 [docs/platform-progress.md](docs/platform-progress.md)。

先检查现有实现及用户未提交修改，复用 Go/Vue/GORM 和既有管理模块；遵循当前任务范围，只报告实际完成与验证的结果。

技能在 `.agents/skills/`（Codex）与 `.claude/skills/`（Claude）之间双向同步，修改任一边后须同步另一边，规则与命令见 [AGENTS.md](AGENTS.md) 的“技能双向同步”。编辑技能后由钩子自动执行 `scripts/sync-skills.sh`，删除或重命名文件时手动运行 `--mirror`。
