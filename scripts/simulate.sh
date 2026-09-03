#!/usr/bin/env bash
# 模拟算法侧上报：心跳 + 人员/车辆/车位事件
# 用法: ./scripts/simulate.sh [host]   默认 http://localhost:8080
set -euo pipefail

HOST="${1:-http://localhost:8080}"
HOST="${HOST%/}"
CT="Content-Type: application/json"

for dependency in curl python3; do
  if ! command -v "$dependency" >/dev/null 2>&1; then
    printf '错误：缺少依赖 %s，模拟未执行。\n' "$dependency" >&2
    exit 1
  fi
done

# 在任何请求之前生成整批时间，统一使用 UTC+08:00，兼容 macOS/Linux。
# 单独检查赋值，避免 JSON 内的命令替换失败后仍发送空时间。
if ! TIMESTAMPS=$(python3 -c '
from datetime import datetime, timedelta, timezone
now = datetime.now(timezone(timedelta(hours=8)))
print(" ".join((now - timedelta(minutes=m)).isoformat(timespec="milliseconds")
               for m in (0, 180, 120, 12, 11, 10, 40, 34, 150, 4, 30, 300)))
'); then
  echo '错误：生成模拟时间失败，模拟未执行。' >&2
  exit 1
fi
read -r NOW AGO_3H AGO_2H AGO_12M AGO_11M AGO_10M AGO_40M AGO_34M AGO_150M AGO_4M AGO_30M AGO_5H <<< "$TIMESTAMPS"

request() {
  local method="$1" path="$2" response status body rc
  local url="$HOST$path"
  local args=(-X "$method" "$url" -H "$CT")
  if [[ $# -eq 3 ]]; then
    args+=(-d "$3")
  fi
  if response=$(curl --silent --show-error --connect-timeout 5 --max-time 30 \
      --write-out $'\n%{http_code}' "${args[@]}"); then
    status="${response##*$'\n'}"
    body="${response%$'\n'*}"
  else
    rc=$?
    printf '错误：%s %s 请求失败（curl 退出码 %s），已停止。请检查服务地址、连接及代理。\n' "$method" "$url" "$rc" >&2
    exit "$rc"
  fi
  case "$status" in
    2[0-9][0-9]) ;;
    *)
      printf '错误：%s %s 返回 HTTP %s，已停止。\n响应：%s\n' "$method" "$url" "$status" "$body" >&2
      exit 1
      ;;
  esac
  if ! python3 -c '
import json, sys
label, path = sys.argv[1:]
def fail(message):
    print(f"错误：{label} {message}，已停止。", file=sys.stderr)
    sys.exit(1)
try:
    response = json.load(sys.stdin)
except ValueError:
    fail("返回空响应或无效 JSON")
if not isinstance(response, dict) or "code" not in response:
    fail("响应缺少业务状态码 code")
if response["code"] != 0:
    code = response["code"]
    message = response.get("message", "未知错误")
    fail(f"业务失败（code={code}）：{message}")
if path == "/api/v1/dashboard":
    data = response.get("data")
    if not isinstance(data, dict) or not isinstance(data.get("stats"), dict):
        fail("响应缺少有效的 data.stats")
    print(json.dumps(data["stats"], indent=2, ensure_ascii=False))
else:
    print(json.dumps(response, ensure_ascii=False))
' "$method $url" "$path" <<< "$body"; then
    exit 1
  fi
}

echo "==> 1. 心跳"
request POST /api/v1/heartbeat "{
  \"device_id\": \"EDGE_BOX_01\",
  \"time\": \"$NOW\",
  \"cameras\": [
    {\"camera_id\":\"CAM_001\",\"status\":\"ONLINE\",\"fps\":12},
    {\"camera_id\":\"CAM_002\",\"status\":\"ONLINE\",\"fps\":11},
    {\"camera_id\":\"CAM_003\",\"status\":\"ONLINE\",\"fps\":15}
  ]
}"
echo; echo "==> 2. 事件批量上报（人/车/车位/行为/身份）"
request POST /api/v1/events "[
  {\"event_id\":\"sim-e001\",\"event_type\":\"PERSON_ENTER\",\"occur_time\":\"$AGO_3H\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1024\"},
   \"person\":{\"identity\":{\"status\":\"UNRESOLVED\"}}},
  {\"event_id\":\"sim-e002\",\"event_type\":\"IDENTITY_UPDATE\",\"occur_time\":\"$AGO_3H\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1024\"},
   \"person\":{\"identity\":{\"status\":\"IDENTIFIED\",\"identity_id\":\"EMP_10086\",\"confidence\":0.91}}},
  {\"event_id\":\"sim-e003\",\"event_type\":\"PERSON_ENTER\",\"occur_time\":\"$AGO_2H\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1108\"},
   \"person\":{\"identity\":{\"status\":\"IDENTIFIED\",\"identity_id\":\"EMP_10023\",\"confidence\":0.88}}},
  {\"event_id\":\"sim-e004\",\"event_type\":\"PERSON_ENTER\",\"occur_time\":\"$AGO_12M\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1031\"},
   \"person\":{\"identity\":{\"status\":\"UNRESOLVED\"}}},
  {\"event_id\":\"sim-e005\",\"event_type\":\"IDENTITY_UPDATE\",\"occur_time\":\"$AGO_11M\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1031\"},
   \"person\":{\"identity\":{\"status\":\"STRANGER\",\"confidence\":0.95}}},
  {\"event_id\":\"sim-e006\",\"event_type\":\"BEHAVIOR\",\"occur_time\":\"$AGO_10M\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1031\"},
   \"person\":{\"identity\":{\"status\":\"STRANGER\"},\"behavior\":\"DANGER_ZONE\"}},
  {\"event_id\":\"sim-e007\",\"event_type\":\"BEHAVIOR\",\"occur_time\":\"$AGO_40M\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1024\"},
   \"person\":{\"identity\":{\"status\":\"IDENTIFIED\",\"identity_id\":\"EMP_10086\"},\"behavior\":\"NO_HELMET\"}},
  {\"event_id\":\"sim-e008\",\"event_type\":\"VEHICLE_IN\",\"occur_time\":\"$AGO_34M\",\"camera_id\":\"CAM_002\",
   \"track\":{\"type\":\"VEHICLE\",\"track_id\":\"CAM002-T-88\"},
   \"vehicle\":{\"plate_no\":\"SNE1234A\",\"confidence\":0.93}},
  {\"event_id\":\"sim-e009\",\"event_type\":\"SPOT_CHANGE\",\"occur_time\":\"$AGO_34M\",\"camera_id\":\"CAM_002\",
   \"track\":{\"type\":\"VEHICLE\",\"track_id\":\"CAM002-T-88\"},
   \"vehicle\":{\"plate_no\":\"SNE1234A\",\"confidence\":0.93},
   \"spot\":{\"spot_id\":\"A-01\",\"status\":\"OCCUPIED\"}},
  {\"event_id\":\"sim-e010\",\"event_type\":\"VEHICLE_IN\",\"occur_time\":\"$AGO_150M\",\"camera_id\":\"CAM_002\",
   \"track\":{\"type\":\"VEHICLE\",\"track_id\":\"CAM002-T-71\"},
   \"vehicle\":{\"plate_no\":\"SBA3307T\",\"confidence\":0.9}},
  {\"event_id\":\"sim-e011\",\"event_type\":\"SPOT_CHANGE\",\"occur_time\":\"$AGO_150M\",\"camera_id\":\"CAM_002\",
   \"track\":{\"type\":\"VEHICLE\",\"track_id\":\"CAM002-T-71\"},
   \"vehicle\":{\"plate_no\":\"SBA3307T\",\"confidence\":0.9},
   \"spot\":{\"spot_id\":\"A-06\",\"status\":\"OCCUPIED\"}},
  {\"event_id\":\"sim-e012\",\"event_type\":\"VEHICLE_IN\",\"occur_time\":\"$AGO_4M\",\"camera_id\":\"CAM_003\",
   \"track\":{\"type\":\"VEHICLE\",\"track_id\":\"CAM003-T-19\"},
   \"vehicle\":{\"plate_no\":\"\",\"confidence\":0}},
  {\"event_id\":\"sim-e013\",\"event_type\":\"SPOT_CHANGE\",\"occur_time\":\"$AGO_30M\",\"camera_id\":\"CAM_002\",
   \"spot\":{\"spot_id\":\"A-05\",\"status\":\"BLOCKED\"}},
  {\"event_id\":\"sim-e014\",\"event_type\":\"VEHICLE_IN\",\"occur_time\":\"$AGO_5H\",\"camera_id\":\"CAM_003\",
   \"track\":{\"type\":\"VEHICLE\",\"track_id\":\"CAM003-T-01\"},
   \"vehicle\":{\"plate_no\":\"SGP1100Z\",\"confidence\":0.95}},
  {\"event_id\":\"sim-e015\",\"event_type\":\"VEHICLE_OUT\",\"occur_time\":\"$AGO_3H\",\"camera_id\":\"CAM_003\",
   \"track\":{\"type\":\"VEHICLE\",\"track_id\":\"CAM003-T-01\"},
   \"vehicle\":{\"plate_no\":\"SGP1100Z\",\"confidence\":0.95}}
]"
echo; echo "==> 3. 幂等验证（重复上报同一批，应全部计入 duplicated）"
request POST /api/v1/events "[
  {\"event_id\":\"sim-e001\",\"event_type\":\"PERSON_ENTER\",\"occur_time\":\"$AGO_3H\",\"camera_id\":\"CAM_001\",
   \"track\":{\"type\":\"PERSON\",\"track_id\":\"CAM001-T-1024\"},
   \"person\":{\"identity\":{\"status\":\"UNRESOLVED\"}}}
]"
echo; echo "==> 4. 校验失败示例（缺 spot.status，应进 rejected）"
request POST /api/v1/events "[
  {\"event_id\":\"sim-bad01\",\"event_type\":\"SPOT_CHANGE\",\"occur_time\":\"$NOW\",\"camera_id\":\"CAM_002\",
   \"spot\":{\"spot_id\":\"A-09\"}}
]"
echo; echo "==> 5. 人工修正 A-05 为 FREE"
request POST /api/v1/spots/A-05/override \
  '{"status":"FREE","operator":"admin","remark":"锥桶误识别"}'
echo; echo "==> 6. 拉取看板数据（截取 stats）"
request GET /api/v1/dashboard
echo "完成。打开 $HOST 查看看板。"
