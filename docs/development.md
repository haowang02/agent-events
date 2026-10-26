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

## 构建与测试

需要 `go.mod` 中声明的 Go 版本。

```sh
go build ./cmd/agent-events
go test ./...
```

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
