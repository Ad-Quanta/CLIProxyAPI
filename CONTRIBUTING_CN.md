# 贡献指南

## 分支

`main` 是稳定分支，只通过 PR 合并。日常开发使用短期分支：

```text
feature/<名称>  新功能
fix/<名称>      缺陷修复
docs/<名称>     文档
chore/<名称>    工程维护
```

从最新 `main` 创建分支：

```bash
git fetch origin
git switch main
git pull --ff-only
git switch -c feature/<name>
```

不要直接在 `main` 上开发。一个分支和一个 PR 只处理一个主题。

## 提交

提交标题使用简短英文和统一前缀：

```text
feat(scope): add ...
fix(scope): correct ...
test(scope): add ...
docs(scope): update ...
chore(scope): maintain ...
```

不要提交凭证、OAuth 文件、数据库、日志、本地部署文件或生产配置。

## PR

PR 需要说明：改了什么、为什么改、如何测试，以及是否影响配置、Home、兼容性或部署。不要混入无关格式化和临时调试代码。

修改重试、fallback、流式、payload 或 Home 协议时，必须补回归测试。

## 提交前检查

```bash
git status
git diff
gofmt -w <changed-go-files>
go test ./...
go build -o test-output ./cmd/server
rm -f test-output
git push -u origin <branch-name>
```

`origin` 是团队仓库，`upstream` 是官方仓库。
