# 简历诊断结构化输出修复与同步记录

## 问题对象

DOCX 文字提取成功，但简历诊断约 40 秒后返回 503。后端错误是 `invalid structured response after one compatibility fallback` 和 `unexpected EOF`；页面却统一显示“多模态不可用”。原请求没有记录 finish_reason，不能确认是输出截断、推理额度耗尽，还是模型返回了不完整 JSON。

## 交付范围

- R01：简历诊断专用模型配置、JSON 输出和截断识别。
- R02：完整简历文字和页面图片输入；纯文字与多模态报告契约。
- R03：安全中文错误分类及原文保留。
- R04：将修复、回归测试和配置说明同步到 Aide，验证并推送 GitHub main。

## 完成标准

- A01：复现旧兼容降级后的 EOF；新版直接 JSON 请求能接收完整有效报告，专用设置不影响其他 Agent。
- A02：拒绝截断、空正文、非法 JSON 和无效报告；不把半截内容显示为诊断结果，不隐式扩大输出额度重复付费。
- A03：请求保留完整原文，纯文字与图片诊断分别按 text_only / multimodal 验证。
- A04：明确区分报告不合法、截断、超时、认证、余额和限流；不泄露供应商原始错误正文，失败不清空页面输入。
- A05：Aide 后端回归通过；前端构建验证有明确结果；同步适配 Aide 导入路径、配置前缀和既有服务绑定设置。

## 实施进度

- [x] 确认 GitHub 目标为 Miaoxiaobai03/Aide 的 main；先快进更新远端已有 README 和 SKILL.md 修改。
- [x] R01 / A01：迁入独立模型配置、专用 Runtime 和 JSON 输出分支；导入路径为 aide/backend，配置前缀为 AIDE_。
- [x] R01、R03 / A02、A04：迁入 finish_reason 检查、结构化错误类型和 HTTP 中文分类。
- [x] R02 / A03：迁入 expectedMode、完整 JSON 与精炼报告指令；保留原文和图片传递机制。
- [x] R04：迁入旧错误复现、配置隔离、文本／图片、截断及 HTTP 错误回归测试。
- [x] R04 / A05：Aide 后端测试与前端构建验证。
- [x] R04：整理 GitHub 交付文件与实施记录；推送结果可通过对应 GitHub 提交记录核实。

## 代码与功能对应

| 文件 | 交付范围与功能 |
| --- | --- |
| backend/internal/resumediagnosis/model_config.go | R01：默认 8192 独立输出额度；仅官方 DeepSeek 启用 json_object 和关闭推理；禁用专用客户端网络自动重试与格式修复 |
| backend/cmd/aide-api/main.go | R01：创建专用诊断客户端与 Runtime；记录非敏感运行参数；保留 AIDE_API_BIND 和本地绑定行为 |
| backend/internal/llm/config.go、client.go | R01、R03：可选 JSON 模式及 provider 扩展；读取结束原因与输出 usage；拒绝截断、空正文和非法结构 |
| backend/internal/resumediagnosis/agent.go | R02、R03：显式诊断模式、完整 JSON 指令、精炼报告和无效语义结果分类 |
| backend/internal/httpapi/resume_diagnosis.go | R03：安全中文错误分类，区分供应商配置与报告问题 |
| web/src/app/api/resume/route.ts | R03：连接失败提示准确；继续透传后端分类结果 |
| .env.example | R01：AIDE_RESUME_MAX_TOKENS、AIDE_RESUME_DISABLE_THINKING、AIDE_RESUME_DIAGNOSTICIAN_TIMEOUT |
| backend/internal/llm/output_failure_test.go | A02：半截和外观合法的 length 输出均拒绝，失败不修改输出对象、不自动格式重试 |
| backend/internal/resumediagnosis/model_config_test.go | A01、A03：旧 EOF 复现、新请求成功、完整原文与图片保留、配置及域名隔离 |
| backend/internal/httpapi/resume_diagnosis_test.go | A04：状态码和中文错误分类、原文保留、不泄露供应商正文 |

对于当前官方 DeepSeek 配置，每次诊断最多一次正常请求加一次既有语义修复；语义修复只针对已经解析成功但不满足内容约束的报告。其他 Agent 的重试、推理和输出配置不变。

## 验证与边界

原开发目录已通过全部 Go 测试和前端构建。本次同步已在 Aide 目录重新验证：

| 检查 | 结果 | 对应标准 |
| --- | --- | --- |
| backend：go test ./... | 全部通过，包括 LLM、简历 Agent、HTTP 回归及其他 Agent | A01–A05 |
| web：npm ci --no-audit --no-fund | 按 Aide 原锁文件安装成功，未改依赖版本或锁文件 | A05 |
| web：npm run build | Next.js 16.4.0 编译、TypeScript 检查及页面生成全部通过 | A05 |
| Aide 命名和服务绑定检查 | 新文件导入路径与配置使用 Aide 命名，AIDE_API_BIND / net.JoinHostPort 保留 | A05 |

构建中的 React 18 弃用提示来自现有依赖，不影响本次构建；本次仅同步诊断修复。前端构建生成的 next-env.d.ts 路径变化不纳入交付。没有新增哈希校验或独立冒烟体系。

旧 EOF 和新请求成功的对照使用本地假模型及合成简历；不代表真实质量、延迟或费用提升。没有上传真实用户简历、API 密钥、数据库、运行二进制或个人运行日志到 GitHub；本轮不调用收费模型。

相关供应商依据：[DeepSeek JSON Output](https://api-docs.deepseek.com/guides/json_mode/)、[Thinking Mode](https://api-docs.deepseek.com/guides/thinking_mode/)。真实供应商仍可能失败；本次没有把本地模拟成功表述为真实模型恢复。
