# 项目说明

- 项目名称：lead-mind-ai-agent
- 项目背景：一个 SaaS AI Agent 项目
- 技术栈：Go 1.25.6、CloudWeGo Eino、Gin、GORM、Viper、go-redis、slog
- 项目介绍、架构与目录结构：[README.md](README.md)

## 开发规范

- Go 开发规范：[docs/GO_STYLE_GUIDE.md](docs/GO_STYLE_GUIDE.md)
- Git 提交规范：[docs/GIT_COMMIT_GUIDE.md](docs/GIT_COMMIT_GUIDE.md)

## Agent 框架

项目统一使用 [CloudWeGo Eino](https://github.com/cloudwego/eino) 构建 Agent 和 LLM 应用。具体的 Eino 使用原则、依赖方向和 SaaS 多租户约束以 [README.md](README.md) 为准。

## 项目强制约束

1. 发现需求、实现方案或现有代码与项目规范冲突时，必须停止相关实现，说明冲突内容、影响和可选方案，并取得人工确认后才能继续；不得自行忽略、覆盖或修改规范。
2. 修复 Bug 或新增功能时，必须同步更新 [README.md](README.md)，记录变更内容、使用方式以及必要的兼容性或迁移说明；文档未同步视为变更未完成。
3. 引入任何新的第三方 Go package、服务、SDK、工具或运行时依赖前，必须说明引入原因、版本、许可证、维护情况、影响和可替代方案，并取得人工确认后才能添加依赖。
4. 设计和实现代码时，必须同时考虑可扩展性、可维护性和代码可读性；优先使用清晰、简单、职责明确的设计，避免过度抽象、重复实现和不必要的耦合。
5. 代码注释统一使用中文。关键业务规则、边界条件、并发控制、错误处理、安全限制、兼容性处理以及不直观的设计决策必须写明注释；注释应重点解释“为什么”和约束条件，不机械复述代码。
