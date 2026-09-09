# 模型级推理档位与 BeeAPI 联调契约

核对日期：2026-09-10。本文描述 GetBeeAPI 本地实现；新增服务端字段是向后兼容的可选扩展，不表示 BeeAPI 生产环境已实现或上线。

## 选择规则

菜单取 **模型/所选 Key 的协议能力 ∩ 原生工具可表达的设置**，不根据名称包含 `max`、`reasoner` 或 `thinking` 猜测能力。

1. `model-options.reasoning` 存在时，以当前协议的声明为准，包括禁用、缺少该协议或空对象；不扩展成模型全量档位。
2. 字段缺省/null（旧服务端）时，模型必须具有 `capabilities: ["reasoning"]` 才使用已核对的官方模型规则。旧 `/v1/models` 兼容路径没有能力声明，只对已知模型/协议启用规则。
3. 自定义模型别名和未核实的跨协议转换需要服务端明确声明。没有可确认档位时说明原因，继续配置连接并保留工具/模型默认行为，不阻断工具配置。
4. 按原生强度排序，展示真实最高档位；`max` 标注更高耗时和用量。推荐值不统一设为最高。`0 / auto` 不写推理覆盖；显式 `none` 表示关闭推理，两者不同。
5. 保存 `reasoning_efforts` 以及绑定模型和协议的 `reasoning_selections` 快照。离线启用按快照校验；模型/Key/入口重新配置会重新计算。无效旧档位不会静默转换：交互重新选择，命令式重新配置提示并移除失效覆盖，直接启用失效保存方案会提示编辑，且不修改原生文件。

## 各工具写入

| 工具 | 字段与限制 |
| --- | --- |
| Claude Code | 普通档位写 `effortLevel`，并使用 `env.CLAUDE_CODE_EFFORT_LEVEL` 覆盖可能残留的模型级默认值。`max` 只写环境变量，移除顶层 `effortLevel`；开启思考并移除固定预算模式覆盖，避免 max 被关闭思考模式降级。切换其他档位/默认行为会更新/删除该环境覆盖。 |
| Codex | `model_reasoning_effort`。仅当本机导出的 `ReasoningEffort` schema 确认支持 max（枚举包含 max，或新式非空字符串类型）才提供和写入 max；探测失败/未安装时保守隐藏，保留其他已支持档位。 |
| Gemini CLI | 专用 `getbeeapi` alias 下的 `generateContentConfig.thinkingConfig`。Gemini 3 系列使用 `thinkingLevel`，2.5 使用 `thinkingBudget`，预算预设展示具体 Token 数，不伪装成 max effort。 |
| Grok Build | `model.beeapi.reasoning_effort` 和实际可选的 `reasoning_efforts`。当前已核实原生表达范围为 low/medium/high/xhigh，不擅自声称该工具已支持任意模型的 max。 |
| OpenCode | 保留 `@ai-sdk/openai-compatible` 的 Chat 协议，模型 `options.reasoningEffort`；该字段是 provider-dependent 字符串，不切换协议来冒充 max 支持。 |
| OpenClaw | `agents.defaults.thinkingDefault` 与该模型的 `compat.supportedReasoningEfforts` 使用实际能力，不写通用假枚举。关闭推理的菜单值 `none` 对应原生 `off`。 |
| Hermes | `agent.reasoning_effort`，保留 custom provider、原生连接和其他 YAML 配置。 |
| Claude Desktop | 暂无确认的持久化字段，不提供推理档位选项。 |

Codex 探测运行本机 `codex app-server generate-json-schema --out <临时目录>`，不创建对话或调用模型；超时有界、输出不展示，临时目录自动清理，同一进程按可执行文件缓存结果。原生工具缺失不阻止预配置其余字段。不同工具及其旧版本仍需真实环境联测；文件投影测试不等同于模型服务端已经执行相应推理档位。

所有写入继续通过既有配置备份/回滚机制，只修改 BeeAPI 连接和用户所选的推理字段；不修改 MCP、权限、主题等无关设置。不增加推理 API 调用来探测能力或消耗余额。

## BeeAPI 可选字段（v1 提案）

两个接口内的每个模型使用相同字段：

- `GET /api/v1/client/model-options`：粘贴 API Key。
- `GET /api/v1/oauth/api-keys/:id/model-options`：OAuth 账户连接。

```json
{
  "id": "merchant-coding-model",
  "protocols": ["openai/responses", "openai/chat_completions"],
  "capabilities": ["reasoning", "tools"],
  "reasoning": {
    "openai/responses": {
      "mode": "effort",
      "supported_efforts": ["low", "high", "max"],
      "default_effort": "high"
    },
    "openai/chat_completions": {
      "mode": "effort",
      "supported_efforts": ["high"],
      "default_effort": "high"
    }
  }
}
```

协议键沿用现有 `protocols`：`anthropic/messages`、`openai/responses`、`openai/chat_completions`、`gemini/contents`。

| 字段 | 语义 |
| --- | --- |
| `mode: "effort"` | Anthropic/OpenAI 协议上的原生推理强度，工具使用自身对应字段；服务端保证实际转换。 |
| `mode: "thinking_level"` | Gemini 原生等级；不是 OpenAI 的 `reasoning_effort`。 |
| `mode: "budget"` | Gemini 预算；必须同时提供 `budgets`，例如 `{"low":1024,"medium":8192,"high":32768}`。CLI v1 接受 0–32768 的预算预设，不接受任意字段/代码。 |
| `mode: "none"` | 禁用手动档位选择；不等于模型完全不能思考，可表示思考始终开启但不可调节。 |
| `supported_efforts` | 真实生效档位，不将兼容别名列成不同强度。例如 DeepSeek V4 原生为 low/high/max，不把映射为 high 的 medium/xhigh 伪装成额外档位。 |
| `default_effort` | 可选，必须属于支持集合，用作此线路的推荐值。 |

CLI v1 认识 none/minimal/low/medium/high/xhigh/max。未知 mode、未知/重复档位、无效默认值或预算会禁用该声明的手动选择，不升级权限或回退猜测。旧服务端省略整个字段；明确禁用用 `mode:none`，不要用 null。该扩展不改变现有模型排序、路由优先级、OAuth scope 或密钥权限。

BeeAPI 必须按该 Key **实际可能执行的线路和协议转换**生成声明。若会在多个上游间回退，不能简单合并其支持集合；应使用所有可能线路都能保证的有效能力，或提供能够锁定该能力的路由行为。相同模型名在不同商家/Key/协议下可以返回不同档位。不能仅因为官方原生接口支持 max，就声称某个反向代理或兼容转换也支持。

## 官方核对来源

- [OpenAI 模型目录](https://developers.openai.com/api/docs/models)：GPT-6 Astra、GPT-5.6 Sol/Terra/Luna 支持 max；[GPT-5.5](https://developers.openai.com/api/docs/models/gpt-5.5) 最高 xhigh。较早模型按各自公开枚举保留，未知后缀不继承档位。
- [Claude effort](https://platform.claude.com/docs/en/build-with-claude/effort)、[Claude Code 模型配置](https://code.claude.com/docs/en/model-config)：max 与 xhigh 支持范围不同；max 不接受写入普通 effortLevel/modelSettings。
- [DeepSeek 思考模式](https://api-docs.deepseek.com/zh-cn/guides/thinking_mode/)：V4 原生 low/high/max，xhigh 并不是 max。
- [智谱思考能力](https://docs.bigmodel.cn/cn/guide/capabilities/thinking)：原生 GLM-5.2 提供 high/max 等兼容输入；不将不同平台托管的旧 GLM 推广成统一原生规则。
- [Kimi 模型选择](https://www.kimi.ai/help/kimi-api/api-model-selection)：K3 支持 low/high/max。K2 系列不能类推。
- [Grok reasoning](https://docs.x.ai/developers/model-capabilities/text/reasoning)：4.6 最高 xhigh；4.5 最高 high。
- [Qwen 百炼 Chat](https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-chat-completions)：Qwen3.8 的原生等级为 low/medium/xhigh，max 是兼容映射；不同时写 effort 与 thinking_budget。
- [Gemini thinking](https://ai.google.dev/gemini-api/docs/thinking)：3.x 最高 high，可用等级按型号区分。
- [MiniMax OpenAI API](https://platform.minimax.io/docs/api-reference/text-openai-api)：M3 提供 adaptive/disabled；M2.x 始终思考，不伪造 max 等级。
- [OpenCode 模型配置](https://opencode.ai/docs/models/)、[OpenAI-compatible SDK](https://ai-sdk.dev/providers/openai-compatible-providers)、[OpenClaw thinking](https://docs.openclaw.ai/tools/thinking)、[Hermes 配置](https://hermes-agent.nousresearch.com/docs/user-guide/configuration/)：用于核对原生字段，而不是把模型强度和工具菜单等同。
