# Workshop Server · 维修车间智能监控服务端（MVP）

Go 单体 + SQLite（WAL）+ `go:embed` 内嵌看板页面。对接算法侧事件上报，提供实时看板和全部人员历史观测查询。

页面支持“实时看板 / 历史中心”切换及 EN / 中文切换，浏览器会记住语言选择。历史中心可按日期、人员类型、关键词、摄像头、区域和安全告警查询已识别员工、陌生人员与待识别人员；时间线只展示已上报的观测，不推断跨摄像头的连续行走路线。语言回归测试：`node --test scripts/test_i18n.mjs`（使用 Node.js 内置测试工具，无额外依赖）。

## 快速开始

```bash
# 需要 Go 1.26.3+
go build -o workshop-server .

./workshop-server          # 默认监听 :8080，无鉴权，仅限可信网络
# 打开浏览器访问 http://localhost:8080
```

环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `WS_ADDR` | `:8080` | 监听地址 |
| `WS_DB` | `data/workshop.db` | SQLite 路径（WAL 模式） |
| `WS_SNAPSHOT_DIR` | `data/snapshots` | 告警截图存储目录 |

## 模拟联调（无需算法侧即可演示）

```bash
./scripts/simulate.sh http://localhost:8080
```

脚本依次验证：心跳上报 → 15 条混合事件（人员/陌生人/行为告警/车辆/车位）→ 幂等重传 → 参数校验拒绝 → 人工修正 → 看板聚合数据。

## 接口一览（协议 v1.1）

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/api/v1/events` | 事件批量上报（1~100 条，`event_id` 幂等） |
| POST | `/api/v1/heartbeat` | 边缘设备心跳 + 摄像头状态 + 时钟偏差检测 |
| GET | `/api/v1/dashboard` | 看板聚合（人员/车辆/车位/告警/统计） |
| GET | `/api/v1/events` | 事件历史查询（event_type / camera_id / track_id / identity_id / 时间范围 / 分页） |
| GET | `/api/v1/history/person-visits` | 人员历史汇总（identity_id / identity_status / keyword / camera_id / area_id / alert_only / 时间范围） |
| GET | `/api/v1/history/person-visits/{key}` | 单个人员观测时间线（支持 person_key 与旧 identity_id） |
| POST | `/api/v1/spots/{spot_id}/override` | 车位人工修正（status 与 reason_code 必填，自动落 OPERATOR_OVERRIDE 事件） |

鉴权已取消：所有接口均无需 `Authorization`，旧的 `WS_TOKEN` 环境变量不再生效。能访问服务的人都可读取数据、上报事件和人工修改车位。本机使用建议设置 `WS_ADDR=127.0.0.1:8080`。本次按用户明确要求开放公网演示，所有访问者均可读写，不应存放真实人员信息或敏感截图；正式生产应另行限制访问。

## 关键设计

- **事件流水 append-only**：`events` 表只插不改，`event_id` 唯一索引实现幂等，算法侧重试/补传安全
- **在场状态纯推导**：在场人员/车辆由 ENTER/LEAVE 事件折叠得出，无冗余状态表
- **身份三级演进**：`PERSON_ENTER(UNRESOLVED)` → `IDENTITY_UPDATE(IDENTIFIED/STRANGER)`，看板展示最新结论
- **未知人员不误合并**：陌生人员与待识别人员按单次观测分段展示；后续识别成功时整段回填到已识别身份
- **车位状态驱动**：`SPOT_CHANGE` 事件实时驱动 `parking_spots`，人工修正标记 `overridden`，下一条算法事件自动恢复算法驱动
- **人工修正可审计**：修正必须传合法的 `reason_code`；`OTHER` 必须附备注，其他备注最长 200 个 Unicode 字符
- **兜底规则**：ENTER 超 30 分钟无 LEAVE 标记 `stale`（待确认）；摄像头 60 秒无心跳判离线
- **人脸库在算法侧**：服务端只存 `identity_id → 姓名/角色` 映射（`identities` 表），不接触人脸数据

## 目录结构

```
workshop-server/
├── main.go          # 入口、路由、embed 静态页
├── db.go            # SQLite(WAL) 初始化、表结构、种子数据
├── handlers.go      # 事件上报 / 心跳 / 人工修正 / 事件查询
├── aggregate.go     # 看板聚合：在场推导、统计、告警
├── history.go       # 员工轨迹分段、身份回填与历史查询
├── static/index.html  # 看板页面（方案 D，3 秒轮询 /api/v1/dashboard）
├── scripts/simulate.sh  # 模拟算法侧上报的联调脚本
└── data/            # 运行时生成：workshop.db + snapshots/
```

## 已知边界（MVP 接受）

- 车位超时阈值目前统一 120 分钟，改 `parking_spots.target_minutes` 即可
- 告警的 `notify_status` 恒为 `NONE`，WhatsApp 推送未实现（字段已预留）
- "今日"统计按服务器本地时区零点切分
- 截图以 base64 随事件上报，单条限 2MB

## 服务器部署

服务端为 Linux amd64 时，在本地完成测试和交叉编译，不在服务器安装 Go 或编译源码：

```bash
node --test scripts/test_i18n.mjs
python3 scripts/test_simulate.py
go test ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/workshop-server .
shasum -a 256 dist/workshop-server
```

发布目录为 `/opt/workshop-server/releases/<commit>/`，`current` 指向已验证版本；单独上传二进制和 `deploy/workshop-server.service`，在服务器比对 SHA-256 后再启用。systemd 单元安装到 `/etc/systemd/system/workshop-server.service`，通过 `systemctl enable --now workshop-server` 启动，使用 `systemctl status workshop-server` 和 `journalctl -u workshop-server` 检查。

数据由 systemd `StateDirectory` 独立保存到 `/var/lib/workshop-server`（DynamicUser 模式下真实目录由 systemd 管理），不上传本地演示数据库、截图或密钥。首次部署仅含初始化种子数据。后续升级应先停止本服务并备份完整数据目录及当前服务配置，再切换 `current`；回退时停止服务、切回上一 release，若升级涉及表结构变化则同时恢复匹配的数据备份。本次部署不改变表结构。

提供的 systemd 单元只监听 `127.0.0.1:8080`，使用新加坡时区。按用户明确授权，通过独立的 `deploy/workshop-server.nginx.conf` 开放公网 HTTP `18080` 端口，入口为 `http://47.97.66.237:18080`。将配置安装到 `/etc/nginx/conf.d/workshop-server.conf`，先执行 `nginx -t`，通过后 reload，不改其他站点。公网入口无鉴权、无 TLS，不要传输敏感数据；代理请求体上限为 10MB，含多张截图的批次需拆分。云安全组需允许 TCP 18080；部署后必须从服务器外验证可访问性。

也可通过 SSH 隧道访问，例如在本机运行：

```bash
ssh -i /path/to/key.pem -N -L 127.0.0.1:18080:127.0.0.1:8080 root@SERVER_IP
```

然后打开 `http://127.0.0.1:18080`。撤销公网入口时，仅移除本项目的 Nginx 配置并在 `nginx -t` 通过后 reload，后端及数据不受影响。
