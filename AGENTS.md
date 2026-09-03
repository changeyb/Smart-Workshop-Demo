# Repository Guidelines

## 项目结构与模块组织

本项目是 Go + SQLite（WAL）维修车间监控 MVP，根目录统一使用 `package main`。

- `main.go`：启动、路由与静态资源嵌入（API 无鉴权）。
- `db.go`：数据库连接、表结构与种子数据。
- `handlers.go`：事件、心跳、历史查询及车位人工修正。
- `aggregate.go`：在场状态、告警与看板统计。
- `static/index.html`：内含 CSS、JavaScript 的看板，无独立前端构建流程。
- `scripts/`：模拟上报脚本及 Python 回归测试；`data/` 为运行时数据库和截图目录。

## 构建、测试与本地开发

使用 Go 1.26.3 或更高版本；脚本另需 Bash、curl 与 Python 3。在项目根目录执行：

```bash
go build -o workshop-server .       # 构建并嵌入 static/
WS_ADDR=127.0.0.1:8080 ./workshop-server  # 本地启动
gofmt -w main.go db.go handlers.go aggregate.go
go vet ./...                       # Go 静态检查
go test ./...                      # Go 测试入口
python3 scripts/test_simulate.py    # 脚本隔离回归测试
bash scripts/simulate.sh http://127.0.0.1:8080
```

看板地址为 `http://127.0.0.1:8080`。修改静态页后须重新构建并重启。模拟脚本需要已启动的服务，会写入事件并修改车位，只能用于测试实例。

## 编码风格与命名

Go 使用 `gofmt` 的制表符缩进；仅格式化本次修改的文件。导出标识符使用 PascalCase，内部标识符使用 camelCase；JSON 和 SQL 字段使用 snake_case，事件类型沿用 `PERSON_ENTER` 等大写下划线格式。Python 使用四空格缩进，Bash 使用两空格缩进并保留 `set -euo pipefail`。前端沿用现有风格，避免整页无关重排；目前没有额外 lint 配置。

## 测试要求

现有测试使用 Python 标准库 `unittest`，通过临时回环 HTTP 服务验证时间戳、不发送鉴权头与失败即停止行为，不连接业务数据库。新增 Python 用例命名为 `test_*`；新增 Go 测试放在对应模块旁的 `*_test.go`，使用 `testing` 和 `TestXxx`。

`main_test.go` 使用隔离数据库验证无鉴权路由、事件幂等和参数拒绝；当前没有覆盖率门槛，不能将“无测试”视为功能验证。事件逻辑变更应覆盖幂等重传、参数拒绝和状态推导；页面变更需检查真实看板并附截图。

## 提交与 Pull Request

当前目录已初始化 Git，使用 `feat:`、`fix:`、`docs:`、`test:` 前缀，例如 `fix: 修正心跳离线判定`。提交只包含任务相关文件，不纳入运行时数据库、截图、凭据或重新生成的二进制。

PR 应说明目的、影响范围、验证命令与结果；有关联 issue 时附链接，页面变更附截图。涉及接口或表结构时说明兼容性、数据迁移与回退方式。

## 配置与架构边界

配置入口为 `WS_ADDR`、`WS_DB`、`WS_SNAPSHOT_DIR`。按用户要求取消 API 鉴权，`WS_TOKEN` 不再生效；测试使用隔离数据库与截图目录。API、静态页和 `/snapshots/` 均无鉴权。本次用户明确授权公网演示：后端监听 `127.0.0.1:8080`，独立 Nginx 入口为 HTTP 18080；不得上传真实人员信息、敏感截图或本地运行数据库。本机使用仍建议监听 `127.0.0.1`，正式生产需另行明确访问限制。

保留事件流水只追加、`event_id` 幂等及人员/车辆状态由事件推导的设计；不要将人脸数据引入服务端。协作回复统一使用简体中文，未执行的检查必须明确标注。
