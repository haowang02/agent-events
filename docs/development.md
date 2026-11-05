# 开发与发布

## 目录结构

| 目录 | 内容 |
| --- | --- |
| `cmd/agent-events` | 命令入口 |
| `internal/event` | 请求记录的数据结构 |
| `internal/collect` | 查找各 Agent 的数据源并解析出请求记录，每个 Agent 一个文件 |
| `internal/store` | SQLite 存储：请求记录、已解析数据源的签名、自定义价格 |
| `internal/pricing` | 价格解析、models.dev 参考价和计费 |
| `internal/server` | 定时采集、查询统计和 HTTP 接口 |
| `web` | 网页界面，编译时嵌入二进制 |

`web/app.js` 负责应用初始化、Agent 选择与页面调度，各页面分别放在 `events.js`、`analysis.js`、`prices.js`。`controls.js` 提供下拉框、分段切换和弹层交互，`date-range.js` 管理时间范围；DOM 工具和图表绘制分别放在 `dom.js`、`charts.js`。

## 前端约定

界面面向桌面浏览器，不做移动端或窄屏专用适配。桌面窗口或显示区域变化时，仍需保持容器缩放、工具栏换行、统计卡片自动换列及图表自适应；不为窄屏隐藏功能或引入移动端专用交互。下拉与日期时间选择使用项目自定义组件，保持浅色和深色主题一致。

## 构建与测试

需要 `go.mod` 中声明的 Go 版本。

```sh
go build ./cmd/agent-events
go test ./...
```

## Agent 图标

`web/agents.svg` 使用 [Lobe Icons](https://icons.lobehub.com/) 的静态 SVG，来源固定在 [`lobehub/lobe-icons@ea434b96`](https://github.com/lobehub/lobe-icons/tree/ea434b96e75813517b377825e01c0df61dee8836/packages/static-svg/icons)。每个 symbol 前标明原文件名，文件头保留 MIT 许可证。

优先使用彩色版本；只有单色版本时使用 `currentColor` 跟随界面主题。Codex、Claude Code、Gemini CLI 等使用各自产品的图标。Codebuff、Droid、ZCode 暂未被图标库收录，保留字母占位图标。

更新时将对应 SVG 转为 symbol，保留 `viewBox`、填充规则和渐变、滤镜定义，并确保定义的 ID 在整个文件中唯一。图标随网页嵌入二进制，运行时不依赖 CDN。

## 发布

推送 `v` 开头的标签后，GitHub Actions 运行测试，再用 [GoReleaser](https://goreleaser.com/) 构建 macOS、Linux、Windows 的 amd64 和 arm64 版本并发布到 Releases：

```sh
git tag v0.1.0
git push origin v0.1.0
```

本地预览发布产物：

```sh
goreleaser release --snapshot --clean
```
