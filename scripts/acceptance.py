#!/usr/bin/env python3
"""PassPal 端到端验收测试。

直接打真实 HTTP 接口，不使用任何 mock。覆盖：登录与锁定、CSRF/Origin、
项目/标签/账号 CRUD、reveal、批量导入与幂等、搜索、备份，以及
「SQLite 里看不到明文」这条硬要求。
"""
import http.cookiejar
import json
import os
import sqlite3
import sys
import time
import urllib.error
import urllib.request

BASE = os.environ.get("PP_BASE", "http://127.0.0.1:3902")
ORIGIN = BASE
PASSWORD = os.environ.get("PP_PASSWORD", "TestPass123!")
DB_PATH = os.environ["PP_DB"]

cj = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(
    urllib.request.ProxyHandler({}),
    urllib.request.HTTPCookieProcessor(cj),
)

CSRF = None
results = []


def call(method, path, body=None, *, origin=ORIGIN, csrf=True, extra=None):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(BASE + path, data=data, method=method)
    if data is not None:
        r.add_header("Content-Type", "application/json")
    if origin is not None:
        r.add_header("Origin", origin)
    if csrf and CSRF:
        r.add_header("X-CSRF-Token", CSRF)
    for k, v in (extra or {}).items():
        r.add_header(k, v)
    try:
        resp = opener.open(r, timeout=30)
        return resp.status, json.loads(resp.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw or "{}")
        except json.JSONDecodeError:
            return e.code, {"raw": raw}


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("  PASS  " if ok else "  FAIL  ") + name + (("  -> " + detail) if detail else ""))


def main():
    global CSRF

    print("\n[1] 健康检查与鉴权边界")
    code, body = call("GET", "/api/health", origin=None, csrf=False)
    check("GET /api/health 返回 200", code == 200 and body.get("data", {}).get("status") == "ok", f"{code}")

    code, _ = call("GET", "/api/projects", origin=None, csrf=False)
    check("未登录访问 /api/projects 返回 401", code == 401, str(code))

    print("\n[2] 登录")
    code, body = call("POST", "/api/auth/login", {"password": "wrong-pass"}, csrf=False)
    msg = body.get("error", {}).get("message", "")
    check("错误密码返回 401 且提示剩余次数", code == 401 and "剩余尝试次数" in msg, f"{code} {msg}")

    code, body = call("POST", "/api/auth/login", {"password": PASSWORD}, csrf=False)
    CSRF = body.get("data", {}).get("csrf_token")
    check("正确密码登录成功并下发 CSRF", code == 200 and bool(CSRF), str(code))

    code, body = call("POST", "/api/projects", {"name": "x"}, origin=None)
    check("缺少 Origin 的写请求被拒绝", code == 403, str(code))

    code, body = call("POST", "/api/projects", {"name": "x"}, csrf=False)
    check("缺少 CSRF 头的写请求被拒绝", code == 403, str(code))

    print("\n[3] 项目与标签")
    code, body = call("POST", "/api/projects", {"name": "AI账号", "description": "AI 相关"})
    pid = body.get("data", {}).get("id")
    check("创建项目成功", code == 201 and pid, f"{code} id={pid}")

    code, _ = call("POST", "/api/projects", {"name": "AI账号"})
    check("同名项目返回 409", code == 409, str(code))

    code, body = call("POST", f"/api/projects/{pid}/tags", {"name": "Gemini", "color": "#0F766E"})
    tid = body.get("data", {}).get("id")
    check("创建标签成功", code == 201 and tid, f"{code} id={tid}")

    code, body = call("GET", "/api/projects")
    proj = next((p for p in body["data"]["items"] if p["id"] == pid), None)
    check("项目列表带账号数",
          code == 200 and proj is not None and proj["account_count"] == 0,
          str(code))

    print("\n[4] 账号 CRUD 与取密")
    code, body = call("POST", "/api/accounts", {
        "project_id": pid, "email": "a1b2c3@gmail.com", "username": "alice",
        "password": "SuperSecret!123", "backup_email": "backup@gmail.com",
        "f2a": "JBSWY3DPEHPK3PXP", "credential_json": '{"k":"v"}',
        "notes": "自用", "tag_ids": [tid],
    })
    aid = body.get("data", {}).get("id")
    blob = json.dumps(body)
    check("创建账号成功", code == 201 and aid, f"{code} id={aid}")
    check("创建响应不含明文密码", "SuperSecret!123" not in blob and "password_encrypted" not in blob)

    code, body = call("GET", "/api/accounts")
    blob = json.dumps(body)
    check("列表返回 has_* 标记", body["data"]["items"][0].get("has_password") is True)
    # 设计变更：F2A / Refresh Token / 取码链接 随列表返回明文
    # （单人本地工具的使用便利性取舍，见 README「列表可见字段」）。
    # 密码、JSON 凭证、备注仍然不下发，必须走 reveal 接口。
    check("列表不下发密码明文", "SuperSecret!123" not in blob)
    check("列表不下发密文列名", "password_encrypted" not in blob)
    check("列表带回 F2A 明文", body["data"]["items"][0].get("f2a") == "JBSWY3DPEHPK3PXP")

    code, body = call("POST", f"/api/accounts/{aid}/reveal", {"field": "password"})
    check("reveal password 返回正确明文",
          code == 200 and body["data"]["value"] == "SuperSecret!123", str(code))

    code, body = call("POST", f"/api/accounts/{aid}/reveal", {"field": "password_encrypted"})
    check("reveal 非法字段被拒绝", code == 422, str(code))

    code, body = call("GET", f"/api/accounts/{aid}")
    rev = body["data"]["revision"]
    code, body = call("PATCH", f"/api/accounts/{aid}", {"revision": rev, "username": "alice2"})
    check("PATCH 携带正确 revision 成功", code == 200, str(code))

    code, body = call("PATCH", f"/api/accounts/{aid}", {"revision": rev, "username": "alice3"})
    check("PATCH 使用过期 revision 返回 409", code == 409, str(code))

    code, body = call("PATCH", f"/api/accounts/{aid}", {"revision": rev + 1, "password": None})
    check("PATCH 显式 null 清空密码", code == 200, str(code))
    code, body = call("POST", f"/api/accounts/{aid}/reveal", {"field": "password"})
    check("清空后 reveal 返回空串", body["data"]["value"] == "", str(code))

    print("\n[5] 批量导入")
    raw = "\n".join([
        "u1@gmail.com----Pass1!",
        "u2@gmail.com----Pass2!----backup2@gmail.com----JBSWY3DPEHPK3PXP",
        "u3@gmail.com|Pass3!|backup3@qq.com",
        "u1@gmail.com----Pass1!",
        "not-an-email",
    ])
    code, body = call("POST", "/api/import/parse",
                      {"project_id": pid, "tag_ids": [tid], "text": raw})
    d = body.get("data", {})
    prev_id = d.get("preview_id")
    commit_key = d.get("commit_key")
    revision = d.get("preview_revision")
    summary = d.get("summary", {})
    check("解析返回草稿与统计", code == 200 and prev_id and commit_key, str(code))
    check("统计三类互斥且之和等于总数",
          summary.get("total") == summary.get("valid_new", 0) + summary.get("duplicates", 0) + summary.get("invalid", 0),
          json.dumps(summary, ensure_ascii=False))
    check("无效行被识别（缺邮箱）", summary.get("invalid", 0) >= 1, json.dumps(summary, ensure_ascii=False))

    items = d.get("items", [])
    check("预览不含明文密码", all("Pass1!" not in json.dumps(it) for it in items))
    check("预览带 has_password 标记", any(it.get("has_password") for it in items))

    code, body = call("POST", "/api/import/commit", {
        "preview_id": prev_id, "preview_revision": revision,
        "commit_key": commit_key, "strategy": "skip",
    })
    s1 = body.get("data", {}).get("summary", {})
    check("提交成功并返回真实计数", code == 200 and s1.get("created", 0) >= 2, json.dumps(s1, ensure_ascii=False))

    code, body = call("GET", f"/api/accounts?project_id={pid}&page_size=100")
    total_after = body["data"]["total"]

    code, body = call("POST", "/api/import/commit", {
        "preview_id": prev_id, "preview_revision": revision,
        "commit_key": commit_key, "strategy": "skip",
    })
    check("同 commit_key 重试返回 already_committed",
          body.get("data", {}).get("already_committed") is True, str(code))

    code, body = call("GET", f"/api/accounts?project_id={pid}&page_size=100")
    check("幂等：账号数未增加", body["data"]["total"] == total_after,
          f"{total_after} -> {body['data']['total']}")

    print("\n[6] 搜索")
    code, body = call("GET", "/api/search?q=gmail.com")
    check("搜索 gmail.com 命中账号", code == 200 and body["data"]["total"] >= 2, str(code))
    code, body = call("GET", "/api/search?q=Pass1!")
    check("搜索不会命中加密字段内容", body["data"]["total"] == 0, str(code))

    print("\n[7] 备份与导出")
    code, body = call("POST", "/api/backup")
    check("在线备份成功", code == 201 and body["data"]["name"].startswith("backup-"), str(code))
    code, body = call("GET", "/api/backup/list")
    check("备份清单可读", code == 200 and len(body["data"]["items"]) >= 1, str(code))
    time.sleep(2.5)  # 重新认证与登录共用频率预算，先让令牌桶恢复
    code, body = call("POST", "/api/export",
                      {"scope": "all", "format": "json", "admin_password": "nope"})
    check("导出使用错误密码被拒绝", code == 401, str(code))

    print("\n[8] 数据安全：直接读 SQLite")
    con = sqlite3.connect(f"file:{DB_PATH}?mode=ro", uri=True)
    cur = con.cursor()
    cur.execute("SELECT email, hex(password_encrypted), hex(f2a_encrypted), hex(backup_email_encrypted) "
                "FROM accounts WHERE password_encrypted IS NOT NULL")
    rows = cur.fetchall()
    dump = "".join(str(r) for r in rows)
    check("库中存在加密字段", len(rows) > 0, f"{len(rows)} 行")
    for marker in ["SuperSecret!123", "Pass1!", "Pass2!", "Pass3!"]:
        check(f"库中无明文 {marker!r}", marker not in dump)
    cur.execute("SELECT summary_json FROM import_batches LIMIT 5")
    batches = "".join(str(r[0] or "") for r in cur.fetchall())
    check("批次摘要不含密码", "Pass1!" not in batches and "Pass2!" not in batches)
    check("批次摘要不含原文", "----" not in batches)
    con.close()

    print("\n[9] 软删除与回收站")
    code, body = call("DELETE", f"/api/accounts/{aid}")
    check("删除账号成功", code == 200, str(code))
    code, body = call("GET", "/api/trash?type=account")
    check("回收站能看到账号", code == 200 and body["data"]["total"] >= 1, str(code))
    code, body = call("POST", f"/api/accounts/{aid}/restore")
    check("恢复账号成功", code == 200, str(code))

    print("\n[10] 登录锁定（按来源）")
    time.sleep(3)  # 让全局令牌桶恢复，避免与频率限制混淆
    locked = False
    last_code, last_err = None, None
    for _ in range(5):
        code, body = call("POST", "/api/auth/login", {"password": "bad"}, csrf=False)
        last_code = code
        last_err = body.get("error", {}).get("code")
        if code == 429:
            locked = True
            break
        time.sleep(0.7)
    check("连续 5 次错误后按来源锁定",
          locked and last_err == "source_locked", f"{last_code} {last_err}")

    code, body = call("POST", "/api/auth/login", {"password": PASSWORD}, csrf=False)
    err_code = body.get("error", {}).get("code")
    check("锁定期间正确密码也被拒绝",
          code == 429 and err_code == "source_locked", f"{code} {err_code}")

    print("\n" + "=" * 60)
    passed = sum(1 for _, ok, _ in results if ok)
    failed = [n for n, ok, _ in results if not ok]
    print(f"总计 {len(results)} 项，通过 {passed}，失败 {len(results) - passed}")
    if failed:
        print("失败项：")
        for n in failed:
            print("  - " + n)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
