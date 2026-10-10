"""Opt-in real-provider gameplay acceptance against a disposable local stack.

Run with python-ai's venv (httpx, redis and pymysql required). This creates a
disposable account/script and a DB character fixture because PDF parsing does
not extract characters. Never point it at production. No provider credentials
are read or logged here: the running AI service owns them. Checkpoints contain
a login token and must remain in the git-ignored .artifacts directory.
"""
import argparse
import json
import os
import re
import time
from pathlib import Path
from uuid import uuid4

import httpx
import pymysql
import redis

ROOT = Path(__file__).resolve().parents[1]
ARTIFACTS = ROOT / ".artifacts"
FIXTURE_PASSWORD = "Local-acceptance-password-42"


def renew_isolated_session(client, state, sql, persist):
    """Renew only the disposable account, including old access-only checkpoints."""
    identity = None
    if state.get("refresh_token"):
        response = client.post("/api/v1/auth/refresh", json={"refresh_token": state["refresh_token"]})
        if response.status_code == 200:
            identity = response.json()["data"]
        elif response.status_code != 401:
            raise RuntimeError(f"isolated session refresh: HTTP {response.status_code}")
    if identity is None:
        rows = sql("SELECT username,email FROM users WHERE id=%s", (state["user_id"],))
        if len(rows) != 1 or not re.fullmatch(r"real_[a-f0-9]{12}", rows[0]["username"]) or rows[0]["email"] != rows[0]["username"][5:] + "@example.test":
            raise RuntimeError("session renewal is restricted to this script's disposable fixture account")
        response = client.post("/api/v1/auth/login", json={"username": rows[0]["username"], "password": FIXTURE_PASSWORD})
        if response.status_code != 200:
            raise RuntimeError(f"isolated fixture login: HTTP {response.status_code}")
        identity = response.json()["data"]
    if identity.get("user_id") != state["user_id"]:
        raise RuntimeError("renewed session belongs to a different account")
    state.update(token=identity["access_token"], refresh_token=identity["refresh_token"])
    client.headers["Authorization"] = "Bearer " + state["token"]
    persist()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--actions", type=int, default=51)
    parser.add_argument("--fork", action="store_true")
    parser.add_argument("--model", required=True, help="Model configured on the isolated AI service; recorded as provenance")
    parser.add_argument("--allow-isolated-fixture", action="store_true", required=True)
    args = parser.parse_args()
    if not 5 <= args.actions <= 100:
        parser.error("actions must be 5..100")
    ARTIFACTS.mkdir(exist_ok=True)
    checkpoint = ARTIFACTS / "real-game-checkpoint.json"
    output = ARTIFACTS / "real-game-acceptance.json"
    state = json.loads(checkpoint.read_text(encoding="utf-8")) if checkpoint.exists() else {}
    if state.get("completed", 0) > args.actions or state.get("pending_action", {}).get("number", 0) > args.actions:
        parser.error("requested actions cannot precede the existing checkpoint")
    evidence = json.loads(output.read_text(encoding="utf-8")) if output.exists() else {
        "kind": "real_public_gameplay_and_memory", "checks": [], "actions": [],
        "fixture": "Uploaded normal-text.pdf; DB-only predefined character; no synthetic actions or summaries",
        "quality": "Narratives and recall probes require human review; transport success is not factual accuracy",
    }
    if evidence.get("model") and evidence["model"] != args.model and evidence["actions"]:
        parser.error("use a new checkpoint/report for a different model after gameplay begins")
    evidence["model"] = args.model
    evidence["requested_actions"] = args.actions
    evidence["requested_fork"] = args.fork
    evidence["status"] = "running"
    db = pymysql.connect(host="127.0.0.1", port=33284, user="trpg",
        password=os.environ.get("TRPG_ACCEPTANCE_DB_PASSWORD", "optimization-local-test"),
        database="optimization_memory_test", charset="utf8mb4", autocommit=True,
        cursorclass=pymysql.cursors.DictCursor)
    cache = redis.Redis(host="127.0.0.1", port=36379, decode_responses=True)
    client = httpx.Client(base_url="http://127.0.0.1:38080", timeout=195)

    def persist():
        # Write evidence first. If interrupted between the two replacements,
        # replaying the checkpoint uses the same idempotent request and updates
        # its existing evidence entry instead of adding a duplicate action.
        for path, value in ((output, evidence), (checkpoint, state)):
            temporary = path.with_suffix(".tmp")
            temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2, default=str), encoding="utf-8")
            temporary.replace(path)

    def checked(name, **details):
        evidence["checks"].append({"name": name, "passed": True, **details})
        persist()
        print(name, "passed", flush=True)

    def sql(query, params=()):
        with db.cursor() as cursor:
            cursor.execute(query, params)
            return cursor.fetchall()

    def api(method, path, **kwargs):
        response = client.request(method, "/api/v1" + path, **kwargs)
        if response.status_code == 401 and not path.startswith("/auth/") and state.get("user_id"):
            renew_isolated_session(client, state, sql, persist)
            # Preserve the original action/load request ID across renewal.
            response = client.request(method, "/api/v1" + path, **kwargs)
        if response.status_code >= 400:
            raise RuntimeError(f"{method} {path}: HTTP {response.status_code}; " + response.json().get("message", ""))
        return response.json()["data"]

    def memory():
        return api("GET", f"/games/{state['room_id']}/memory-status")

    def wait_summary(position, timeout=240):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            branch = memory()["timeline_id"]
            rows = sql("SELECT * FROM game_summaries WHERE timeline_id=%s AND through_position >= %s ORDER BY version DESC LIMIT 1", (branch, position))
            if rows:
                summary = rows[0]
                assert summary["content"].strip() and len(summary["source_hash"]) == 64
                if cache.get(f"room:{state['room_id']}:summary") != summary["content"]:
                    time.sleep(1)
                    continue
                checked("durable_summary_and_redis_projection", version=summary["version"], through=summary["through_position"], content=summary["content"], source_hash=summary["source_hash"])
                return summary
            time.sleep(2)
        raise RuntimeError("real summary not persisted/projected before deadline")

    def ensure_first_save():
        if "save_id" in state:
            if not state.get("fork_request") and cache.get(f"room:{state['room_id']}:status") == "paused":
                api("POST", f"/games/{state['room_id']}/resume")
            return
        if state["completed"] != 5:
            raise RuntimeError("first summary save must be captured after exactly five actions")
        wait_summary(memory()["head_position"])
        saved = sql("SELECT * FROM game_saves WHERE room_id=%s AND save_name=%s AND is_auto=0 ORDER BY id LIMIT 1", (state["room_id"], "Frozen first summary"))
        if not saved:
            result = api("POST", f"/games/{state['room_id']}/save", json={"save_name": "Frozen first summary"})
            saved = sql("SELECT * FROM game_saves WHERE id=%s", (result["save_id"],))
        state["save_id"] = saved[0]["id"]
        state["frozen_save"] = json.loads(json.dumps(saved[0], default=str))
        persist()
        if cache.get(f"room:{state['room_id']}:status") == "paused":
            api("POST", f"/games/{state['room_id']}/resume")
        checked("manual_save_and_resume", save_id=state["save_id"])

    try:
        deadline = time.monotonic() + 180
        with httpx.Client(timeout=10) as ai:
            while time.monotonic() < deadline:
                try:
                    if ai.get("http://127.0.0.1:38000/ready").status_code == 200:
                        break
                except httpx.TransportError:
                    pass
                time.sleep(2)
            else:
                raise RuntimeError("AI dependencies did not become ready")
        if "token" not in state:
            suffix = uuid4().hex[:12]
            identity = api("POST", "/auth/register", json={"username": "real_" + suffix, "email": suffix + "@example.test", "password": FIXTURE_PASSWORD})
            state.update(token=identity["access_token"], refresh_token=identity["refresh_token"], user_id=identity["user_id"])
            persist()
        client.headers["Authorization"] = "Bearer " + state["token"]
        api("GET", "/users/me")
        if "script_id" not in state:
            with (ARTIFACTS / "m13-fixtures/normal-text.pdf").open("rb") as pdf:
                script = api("POST", "/scripts/upload", data={"title": "Real model memory acceptance"}, files={"file": ("normal-text.pdf", pdf, "application/pdf")})
            state["script_id"] = script["id"]
            persist()
        deadline = time.monotonic() + 300
        while time.monotonic() < deadline:
            script = api("GET", f"/scripts/{state['script_id']}")
            if script["status"] == "ready":
                break
            if script["status"] == "failed":
                raise RuntimeError("real PDF parse failed")
            time.sleep(2)
        else:
            raise RuntimeError("PDF parse deadline exceeded")
        if "character_id" not in state:
            sql("INSERT INTO script_characters(script_id,name,description,attributes) VALUES(%s,%s,%s,%s)", (state["script_id"], "Memory acceptance investigator", "An investigator exploring Blackwood Manor", json.dumps({"hp": 100, "mp": 50, "san": 100, "ac": 10, "level": 1})))
            state["character_id"] = sql("SELECT id FROM script_characters WHERE script_id=%s", (state["script_id"],))[0]["id"]
            checked("real_pdf_index_and_character_fixture", chunks=script["chunk_count"])
        if "room_id" not in state:
            started = time.monotonic()
            result = api("POST", "/games/solo/start", json={"script_id": state["script_id"], "character_id": state["character_id"]})
            state.update(room_id=result["room_id"], completed=0)
            checked("real_opening", seconds=round(time.monotonic()-started, 2), narrative=result["opening_narrative"])
        prompts = [
            "我站在庄园门厅观察四周，并在随身笔记写下自己的约定暗号：青鸦七号。简短描述，不超过100字。",
            "我检查门厅肖像背后，寻找藏着的黄铜钥匙；找到后收入背包。叙事不超过100字。",
            "我查看背包，然后用已有钥匙尝试打开书房的锁门。叙事不超过100字。",
            "我在书房查看残破日记和信件，摘录具体线索到随身笔记。叙事不超过100字。",
            "我检查书房的古钟，比较它与日记的线索，记录关键发现。叙事不超过100字。",
        ]
        exploration = [
            "我在当前位置观察地面脚印的方向，暂时不前进。",
            "我细听门外的雨声与庄园内的异响，保持警觉。",
            "我检查当前位置的墙壁和家具，寻找与庄园历史有关的细节。",
            "我整理已知线索，区分观察到的事实与猜测。",
            "我查看物品和身体状况，决定下一步调查路线。",
            "我在安全处稍作休息，留意照明与周围的变化。",
            "我从当前位置观察相邻通道，暂不进入危险区域。",
            "我谨慎检查附近的门框和窗边，寻找留下的痕迹。",
            "我核对日记中的事件顺序，不重复拾取已经持有的道具。",
            "我在原地整理随身笔记，等待庄园内的动静。",
        ]
        if state.get("completed", 0) >= 5:
            ensure_first_save()
        if state.get("completed", 0) > 5 and state["completed"] % 5 == 0 and not state.get("fork_request"):
            wait_summary(memory()["head_position"])
        if state.get("forked") and state["completed"] < args.actions:
            raise RuntimeError("cannot extend the parent longplay after loading the frozen branch")
        for index in range(state.get("completed", 0), args.actions):
            n = index + 1
            prompt = prompts[index] if index < 5 else exploration[(index-5) % len(exploration)] + "简短叙事，不超过100字。"
            if n == 11:
                prompt = "我在随身笔记新写下一个仅用于今后行程的约定暗号：赤狐九号。简短叙事，不超过100字。"
            if n in (26, 46, 51):
                prompt = "我回想进入庄园最初在笔记写下的约定暗号是什么？请依据已有记忆回答；如果无法确定就说明。回答不超过100字。"
            status = memory()
            turn = int(cache.get(f"room:{state['room_id']}:turn"))
            body = {"request_id": str(uuid4()), "expected_turn": turn,
                "expected_timeline_id": status["timeline_id"], "expected_generation": status["generation"], "action_text": prompt}
            if state.get("pending_action"):
                pending = state["pending_action"]
                if pending["number"] != n:
                    raise RuntimeError("pending action checkpoint is inconsistent")
                body = pending["body"]
                turn = body["expected_turn"]
            else:
                state["pending_action"] = {"number": n, "body": body}
                persist()
            started = time.monotonic()
            # Retry only the same idempotent request; stop after two failed attempts.
            for attempt in range(2):
                try:
                    result = api("POST", f"/games/{state['room_id']}/action", json=body)
                    break
                except (RuntimeError, httpx.TransportError) as exc:
                    evidence.setdefault("failures", []).append({"action": n, "attempt": attempt+1, "error": str(exc)})
                    persist()
                    print(f"action {n} attempt {attempt+1} failed: {exc}", flush=True)
                    if attempt == 1:
                        raise
                    time.sleep(5)
            assert result["current_turn"] == turn + 1 and result["narrative"].strip()
            entry = {"number": n, "request_id": body["request_id"], "timeline_id": body["expected_timeline_id"], "seconds": round(time.monotonic()-started, 2), "input": body["action_text"], "result": result}
            if n in (26, 46, 51):
                entry["recall_probe"] = {"expected": "青鸦七号", "contains_expected": "青鸦七号" in result["narrative"]}
            evidence["actions"] = [a for a in evidence["actions"] if a["request_id"] != body["request_id"]] + [entry]
            state["completed"] = n
            state.pop("pending_action", None)
            persist()
            print(f"action {n}/{args.actions} passed ({entry['seconds']}s)", flush=True)
            if n % 5 == 0 and n != 5:
                latest = memory()
                wait_summary(latest["head_position"])
            if n == 5 and "save_id" not in state:
                ensure_first_save()
        parent = state.get("fork_request", {}).get("parent") or memory()["timeline_id"]
        records = sql("SELECT kind,COUNT(*) AS n FROM game_action_records WHERE room_id=%s AND timeline_id=%s GROUP BY kind", (state["room_id"], parent))
        counts = {row["kind"]: row["n"] for row in records}
        assert counts.get("opening") == 1 and counts.get("action") == state["completed"]
        checked("immutable_archive_counts", records=records)
        current_save = sql("SELECT * FROM game_saves WHERE id=%s", (state["save_id"],))[0]
        assert json.loads(json.dumps(current_save, default=str)) == state["frozen_save"]
        checked("frozen_save_unchanged", later_actions=max(0, state["completed"] - 5))
        if args.fork and not state.get("forked"):
            if "fork_request" not in state:
                state["fork_request"] = {"parent": memory()["timeline_id"], "request_id": str(uuid4())}
                persist()
            parent = state["fork_request"]["parent"]
            loaded = api("POST", f"/games/{state['room_id']}/load", json={"save_id": state["save_id"], "request_id": state["fork_request"]["request_id"]})
            child = memory()["timeline_id"]
            assert child != parent and loaded["turn"] == 5
            assert "赤狐九号" not in (cache.get(f"room:{state['room_id']}:summary") or "")
            state["forked"] = True
            checked("load_creates_branch_with_frozen_summary", parent=parent, child=child, turn=loaded["turn"])
        if args.fork and "branch_probe" not in evidence:
            if cache.get(f"room:{state['room_id']}:status") == "paused":
                api("POST", f"/games/{state['room_id']}/resume")
            status = memory()
            if "branch_action" not in state:
                state["branch_action"] = {"request_id": str(uuid4()), "expected_turn": 5, "expected_timeline_id": status["timeline_id"], "expected_generation": status["generation"], "action_text": "我查看笔记中的约定暗号，列出已知暗号；如果没有记录就说不知道。请勿猜测。回答不超过100字。"}
                persist()
            result = api("POST", f"/games/{state['room_id']}/action", json=state["branch_action"])
            evidence["branch_probe"] = {"result": result, "contains_parent_future_marker": "赤狐九号" in result["narrative"]}
            persist()
        if args.fork:
            assert not evidence["branch_probe"]["contains_parent_future_marker"]
            checked("branch_real_action_no_parent_future_marker")
        evidence.pop("last_error", None)
        evidence["status"] = "completed_requested_checks"
        persist()
    except Exception as exc:
        evidence["status"] = "interrupted"
        evidence["last_error"] = str(exc)
        persist()
        raise
    finally:
        client.close()
        db.close()
        cache.close()


if __name__ == "__main__":
    main()
