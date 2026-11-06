# 重试和模型兜底

本文说明当前请求执行顺序。

## 请求顺序

```text
选择凭证
→ 凭证准备和执行
→ 凭证切换与冷却处理
→ 同一模型的请求重试
→ 错误满足条件时切换模型
→ 返回客户端
```

模型 fallback 通常不会在第一次凭证失败后立即发生。AuthManager 会先完成当前模型适用的凭证切换和请求重试。

实际尝试次数取决于可用凭证、凭证级覆盖配置、冷却状态、Home 调度和请求重试配置。

## 当前 fallback

当前配置逻辑是：

```yaml
model-fallback:
  enabled: true
  preserve-requested-model: true
  trigger-status-codes: [429, 500, 502, 503, 504]
  no-fallback-status-codes: [400, 401, 403, 422]
  rules:
    - from: "gpt-*"
      to: ["grok-4.7", "grok-4.6"]
```

目标按顺序尝试。无效请求、认证失败、请求主动终止和客户端取消不会进入模型 fallback。

## 流式请求

首个有效响应字节发给客户端前：

- 建流失败，进入 AuthManager 的重试处理；
- bootstrap 读取失败，可能额外重试同一模型；
- 最终错误符合条件时，可以切换到下一个模型。

已经向客户端输出内容后，不能再用其他模型接管当前流。

## 响应模型名

开启 `preserve-requested-model` 后，fallback 模型成功返回时，响应中的模型字段会尽量保留客户端请求的 GPT 模型名。内部 usage 和诊断记录仍应记录实际 provider 和模型。

## 修改时必须测试

- 凭证重试先于模型 fallback；
- 状态码允许和禁止列表；
- 多个 fallback 目标的顺序；
- 非流式和流式请求；
- 首字节前的 bootstrap retry；
- 首字节后不再切模型；
- 响应模型名重写；
- Home 的 retry 和 cooldown。
