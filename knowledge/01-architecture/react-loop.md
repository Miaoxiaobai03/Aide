# Agent 架构 - ReAct 循环

## Q：什么是 ReAct 模式？在工程实现中需要注意什么？

> 来源：某大厂 AI 工程师面试

**新手答**："ReAct 就是让模型先思考再行动，循环执行直到完成任务"

**高手答**：

ReAct (Reasoning + Acting) 的核心是将 LLM 的推理能力和外部工具的执行能力结合成一个闭环循环：Observe → Think → Act → Observe。

工程实现中的关键点：

1. **循环终止条件**：不能只靠模型输出 "final_answer" 来终止，必须设置 max_iterations（通常 10-15 轮）作为兜底，否则模型幻觉会导致无限循环，每轮都在烧 token。

2. **Tool 调用的错误恢复**：工具执行失败后，要把结构化的错误信息（不是 stack trace）喂回模型，让它决定是重试、换工具、还是直接给用户一个"我无法完成"的诚实回答。

3. **Context 膨胀管理**：每轮循环都往 messages 数组追加 thought + action + observation，10 轮下来可能已经 50k tokens。必须有压缩策略：要么 sliding window 只保留最近 N 轮，要么对早期轮次做 summarization。

4. **观测和调试**：生产环境下每轮的 thought/action/observation 都要结构化日志输出。如果没有这层观测，出了问题你连模型在第几轮开始"跑偏"都定位不了。

5. **并发安全**：如果 Agent 同时处理多个 session，要确保 Tool 的执行是 session-isolated 的，不能让 A 的搜索结果跑到 B 的 context 里。

**差距在哪**：新手只知道概念层面的 Think-Act，高手关注的是工程落地中的终止保护、错误恢复、资源控制和可观测性。面试官考的是"你有没有真正把这个东西跑到生产"。

## Q：Agent 的 Tool Calling 机制是怎么工作的？Function Calling 和 Tool Use 有什么区别？

> 来源：Agent 架构师面试

**新手答**："就是模型输出一个 JSON，告诉你调哪个函数、传什么参数"

**高手答**：

Tool Calling 的本质是：LLM 在生成过程中，不输出自然语言文本，而是输出一个结构化的"调用意图"（tool_use content block），包含 tool name 和 input JSON。

底层机制差异：
- **OpenAI Function Calling**：模型输出 `function_call` 字段，参数是字符串形态的 JSON，需要你自己 parse。多 tool 时用 `tool_calls` 数组。`finish_reason` 变为 `tool_calls`。
- **Anthropic Tool Use**：模型输出 `tool_use` content block，参数直接是 object（不需要额外 parse）。`stop_reason` 变为 `tool_use`。支持流式增量输出 tool input（`input_json_delta`）。

工程关键点：
1. **Schema 设计**：tool 的 `parameters` JSON Schema 写得越精确，模型越不容易填错参数。`description` 字段对模型的路由决策影响很大。
2. **流式处理**：tool input 可能分多次 delta 送达，你需要一个 buffer 拼接完整 JSON 后再 parse。
3. **并行 tool call**：模型可能在一次回复中请求多个 tool，你需要决定是串行还是并行执行。
4. **Tool result 格式**：返回给模型的 tool result 要简洁、结构化，避免塞一大段 HTML/日志进去污染 context。

**差距在哪**：新手把 tool calling 当"模型输出 JSON"，高手关注的是 schema 工程、流式处理、并行策略和 result 质量控制。

## Q：如何设计一个支持多 Provider 的 LLM 调用层？

> 来源：AI 平台架构面试

**新手答**："写一个 if-else 判断用哪个 SDK 就行"

**高手答**：

多 Provider 统一调用层的核心设计：

1. **统一接口（Provider Interface）**：定义 `stream(params)` 返回 `AsyncIterable<StreamEvent>`，所有 provider 实现这个接口。StreamEvent 是统一的事件枚举：text_delta / tool_use_start / tool_use_delta / tool_use_end / message_end。

2. **Provider Router**：维护 model→provider 的映射表，输入模型名自动路由到对应 provider。支持 fallback 策略（Claude 挂了自动切 GPT-4o）。

3. **Retry + Error Classification**：不同 provider 的错误码不一样（Anthropic 529 vs OpenAI 503），需要统一分类为 rate_limit / overloaded / auth / context_length 等，然后决定是否 retryable。

4. **Token 计数归一化**：各家的计费方式不同（Claude 按 input/output 分计，有 cache token；OpenAI 也类似但字段名不同），需要统一到一个 TokenUsage 结构。

5. **流式适配**：Claude 的 SSE 事件名和 OpenAI 的 chunk 格式完全不同，适配层要屏蔽这些差异，对上层只暴露统一的 StreamEvent。

**差距在哪**：if-else 能跑但不能维护。面试官期望看到接口抽象、错误归一化和流式统一这三个关键设计决策。
