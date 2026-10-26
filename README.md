# agent-events

采集本机 Claude Code、Codex、OpenCode、Antigravity 的模型请求事件，在浏览器中查看、筛选和统计分析。

## 安装

从 [Releases](https://github.com/haowang02/agent-events/releases) 下载对应平台的压缩包，或者：

```sh
go install github.com/haowang02/agent-events@latest
```

macOS 首次运行下载的二进制被拦截时，执行 `xattr -d com.apple.quarantine agent-events`。

## 使用

```sh
agent-events            # 启动后自动在浏览器打开 http://127.0.0.1:8790
```

| 参数 | 说明 |
| --- | --- |
| `-addr` | 监听地址，默认 `127.0.0.1:8790`；端口被占用时自动改用空闲端口 |
| `-data` | 数据目录，默认 `~/.local/share/agent-events`（含 `events.db`、`models-dev.json`） |
| `-no-open` | 不自动打开浏览器 |
| `-rebuild` | 清空已采集事件并重新解析全部日志，保留自定义价格 |

## 数据来源

服务每 3 秒检查一次日志的大小和修改时间，只重新解析有变化的文件；请求数据确实增加或变化时才通知页面刷新。事件保存在 `events.db`，日志被 agent 清理后（如 Claude Code 默认保留 30 天）历史仍然保留。

| Agent | 日志 | 请求 | 延迟 | 首字时间 |
| --- | --- | --- | --- | --- |
| Claude Code | `$CLAUDE_CONFIG_DIR/projects/**/*.jsonl`（默认 `~/.claude`） | 按 `message.id` + `requestId` 合并各内容块 | `api-request` 记录到最后一个内容块；旧版本日志没有该记录，延迟未知 | — |
| Codex | `$CODEX_HOME/{sessions,archived_sessions}/**/*.jsonl`（默认 `~/.codex`） | `token_usage_record` | 从用户消息或上一次 `token_count`（工具调用完成）到本次响应 | 仅每轮第一个请求，来自 `task_complete` |
| OpenCode | `~/.local/share/opencode/opencode.db` | `session_message` 中的 assistant 消息 | `time.created` 到 `time.completed` | — |
| Antigravity | `~/.gemini/antigravity-cli/conversations/*.db` | `gen_metadata`（protobuf，字段按实际数据解析） | 记录中的生成耗时 | — |

Token 统一拆成互不重叠的：非缓存输入、缓存读取、缓存写入、输出（含推理）。TPS = 输出 Token / 总延迟。

## 价格

费用在查询时按当前价格计算，修改价格立即作用于全部历史请求。模型价格依次取：

1. 页面上设置的自定义价格（模型名不区分大小写）。
2. 内置别名指向的模型的价格：`codex-auto-review` → `gpt-5.6-luna`，`gemini-4-argon-trial` → `gemini-4-argon`，`deepseek-v4.1-flash` → `deepseek-v4.1-flash-1017`。
3. [models.dev](https://models.dev/) 参考价（忽略 `provider/` 前缀）：模型厂商自己对该模型 ID 的定价；否则厂商对同名规范模型（`canonical_model_id`）的定价；都没有时，所有提供商一致的定价。价格冲突时视为未定价。

价格可带一档长上下文价格：请求输入超过阈值时，整个请求按该档计价。未设置的缓存读写价格显示为「—」，计费时按输入价计算；Claude 的 1 小时缓存写入按输入价 × 2 计算。参考价缓存超过 24 小时会在启动时自动更新，也可在页面上手动更新。

快速模式（Claude `speed: fast`、Codex `priority`）只在「推理 / 档位」列标出，不改变计价。
