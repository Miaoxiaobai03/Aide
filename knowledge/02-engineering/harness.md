# Harness 工程实践

## Q：什么是 Agent Harness？和 LangChain 这类框架有什么区别？

> 来源：AI 工程师面试

**新手答**："Harness 就是 Agent 的框架，和 LangChain 差不多"

**高手答**：

Agent Harness 是围绕 LLM 构建的**工程基础设施层**，它和框架的本质区别在于：

- **框架**（LangChain/LangGraph）：提供抽象和组合原语，你在它的 API 范式里编程。好处是快速原型，代价是黑盒——出了问题你得去读框架源码理解它帮你做了什么。
- **Harness**：你自己写 Agent Loop，自己控制每一步的逻辑。Harness 不是"另一个框架"，而是"不用框架时你需要自己搭的那些基础设施"。

Harness 10 层模型：
1. **Tools**：工具注册、schema 声明、dispatcher
2. **Skills**：多 tool 编排的复合能力
3. **Query Engine**：LLM 调用、重试、缓存、多 provider 路由
4. **Context**：多层上下文组装、压缩策略
5. **Memory**：长期记忆存取、重要度衰减
6. **Permission**：工具调用的风险分级和审计
7. **Sessions**：对话状态机、checkpoint、rewind
8. **Command**：用户指令解析（/help, /reset 等）
9. **Hook**：pre-tool / post-tool 的治理管线
10. **Sub-agent**：子 Agent 运行时、并发池

为什么选择 Harness 而不是 LangChain：
- 生产环境需要对每一层有完全控制权
- 框架升级可能 break 你的逻辑
- 调试时你知道每一行代码在干什么
- 性能瓶颈可以精确定位到具体层

**差距在哪**：新手把所有 Agent 相关的东西都叫"框架"，高手区分框架和基础设施、理解自建的 trade-off。

## Q：Agent 的错误处理策略应该怎么设计？

> 来源：高级工程师面试

**新手答**："try-catch 捕获异常，打个日志就行"

**高手答**：

Agent 的错误处理比普通后端复杂得多，因为 Agent Loop 是**多轮异步 + 外部依赖密集**的：

1. **分层错误分类**：
   - LLM 层：rate_limit（可重试）、context_length（需压缩后重试）、overloaded（退避重试）、auth（不可重试）
   - Tool 层：tool 执行超时、返回格式错误、权限不足
   - 系统层：OOM、网络断连

2. **错误恢复策略**：
   - LLM 429/529：指数退避 + jitter 重试
   - Context 超限：触发压缩后重试
   - Tool 失败：错误信息结构化后喂回模型，让模型决定下一步
   - 多次重试后仍失败：graceful degradation，告诉用户"这个能力暂时不可用"

3. **Circuit Breaker**：如果某个 provider 连续失败 N 次，自动切到备选 provider，而不是继续打已经挂了的 API。

4. **Checkpoint 恢复**：长对话中如果中间某一步出错，不应该从头重来。Session checkpoint 让你可以从最近的成功状态恢复。

**差距在哪**：try-catch 是最低级的错误处理。面试官期望看到分类、分层恢复策略和 graceful degradation 的设计思维。
