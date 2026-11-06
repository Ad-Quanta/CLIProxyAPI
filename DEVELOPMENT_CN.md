# 开发说明

## 目录速查

- `cmd/`：服务和维护工具入口。
- `internal/api/`：HTTP 路由、处理器和中间件。
- `internal/runtime/`：各 provider 的执行器。
- `internal/translator/`：协议转换。
- `internal/home/`：CLIProxyAPIHome 通信和调度。
- `internal/registry/`：模型注册和更新。
- `internal/watcher/`：配置热加载。
- `sdk/cliproxy/`：可嵌入服务和执行流水线。
- `test/`：跨模块和集成测试。

修改代码前先阅读 `AGENTS.md`，其中的架构、安全、payload、超时和测试要求是硬约束。

## 本地运行和验证

配置模板是 `config.example.yaml`，本地配置使用 `config.yaml`。凭证放在 `auths/`，不能提交。

```bash
go run ./cmd/server
go test ./...
go build -o test-output ./cmd/server
rm -f test-output
```

涉及 fallback 时，优先运行相关包测试；涉及集成流程时检查 `test/fallback/`。测试必须使用 mock 服务和测试凭证，不连接生产账号或 Home。

## 开发原则

1. 先确认请求链路和受影响模块。
2. 检查是否需要同步 CLIProxyAPIHome。
3. 保持改动小，遵守现有模块边界。
4. 新配置增加默认值、规范化和解析测试。
5. payload 在发送前完成最终配置。
6. 流式请求区分首字节前和已经输出内容后的错误。
7. 完成格式化、测试、构建后再提 PR。

## 相关文档

- [贡献指南](CONTRIBUTING_CN.md)
- [重试和兜底](docs/retry-and-fallback_CN.md)
- [CLIProxyAPIHome 协作](docs/home-integration_CN.md)
- [v8 Management API](docs/management-api-v8.md)
- [SDK 文档](docs/sdk-usage_CN.md)
- [Home 部署说明](home/README.md)
- [运维说明](OPS.md)
