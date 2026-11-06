# CLIProxyAPIHome 协作说明

CLIProxyAPIHome 负责 Home 模式下的控制面和调度；CLIProxyAPI 负责请求执行、协议转换、响应处理和 usage 记录。

## 哪些改动必须检查 Home

涉及以下内容时，要同步评估 Home：

- 配置发布和 revision；
- dispatch 请求和响应；
- 凭证选择和并发；
- retry round 和重试上限；
- cooldown 和 `retry-after`；
- 快照、节点、租约和心跳；
- Home 模式管理接口；
- 协议或 capability 协商。

## 冷却和重试

Home 可以把凭证或模型标记为暂时不可用，并记录恢复时间。调度器会改选其他凭证，或者返回带 `retry_after_ms` 的可重试结果。

冷却是凭证/模型可用性调度，不是模型 fallback。通常顺序是：

```text
Home 选择凭证
→ 冷却和并发判断
→ 同一模型的凭证/请求重试
→ 错误符合条件时由 API Handler 触发模型 fallback
```

单个凭证不可用时，不应立即切换模型。

## 兼容要求

- 新字段优先设计为可选字段；
- 旧版本 Home 遇到未知可选字段不能崩溃；
- 新版 CPA 需要明确旧 Home 缺少字段时的降级行为；
- 已发布的 retry、cooldown 和 concurrency 含义不能随意改变；
- 协议变化增加成功和可重试失败的 fixture 测试。

## 协作步骤

1. 先定义协议字段和兼容行为；
2. 在 CPA 增加 fixture 和测试；
3. 在 Home 实现对应变化；
4. 测试新 CPA/旧 Home 和旧 CPA/新 Home；
5. 写明兼容窗口；
6. 两边检查通过后再发布。

Home 部署和恢复见 [Home 部署说明](../home/README.md)，Management API 见 [v8 Management API](management-api-v8.md)。
