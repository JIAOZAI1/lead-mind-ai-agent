# CLAUDE.md

在处理本仓库任何任务前，必须先读取 [PROJECT.md](PROJECT.md) —— 项目背景、目标客户、技术选型、架构分层、多租户安全红线、工程规范与决策记录都在其中，是本项目的唯一权威来源。

架构设计的具体代码模式（FallbackModel、ReAct Agent、审批网关、CheckPoint、Callback 可观测性等）参考 [enterprise-ai-agent-design.md](enterprise-ai-agent-design.md)。两份文档冲突时以 PROJECT.md 为准。

编写或修改 Go 代码前，必须先读取 [go-style-guide.md](go-style-guide.md) —— 基于 Uber Go Style Guide 整理的 Go 开发规范（接口/并发/错误处理/代码风格/性能/常用模式/Lint），是本项目 Go 代码的强制规范来源。
