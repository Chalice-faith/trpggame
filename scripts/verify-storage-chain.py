"""Real Go -> MinIO -> Python -> BGE -> Milvus acceptance (no LLM key).

Run with python-ai's virtualenv. Use a disposable environment only.
Writes redacted evidence, never access tokens or object credentials.
"""
import argparse
import json
import time
from pathlib import Path
from uuid import uuid4
import httpx
from minio import Minio
from minio.error import S3Error


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-url", default="http://127.0.0.1:38080")
    parser.add_argument("--ai-url", default="http://127.0.0.1:38000")
    parser.add_argument("--output", default=".artifacts/storage-acceptance.json")
    args = parser.parse_args()
    import os
    endpoint = os.environ.get("TRPG_AI_MINIO_ENDPOINT", "127.0.0.1:39000")
    access = os.environ.get("TRPG_AI_MINIO_ACCESS_KEY", "minioadmin")
    secret = os.environ.get("TRPG_AI_MINIO_SECRET_KEY", "minioadmin")
    bucket = os.environ.get("TRPG_AI_MINIO_BUCKET", "trpg-scripts")
    storage = Minio(endpoint, access_key=access, secret_key=secret, secure=False)
    evidence = {"kind": "real_storage_and_embedding", "paid_model": "not_executed_missing_key", "checks": []}
    def checked(name, **details):
        evidence["checks"].append({"name":name, "passed":True, **details})
        print(name, "passed", flush=True)
        Path(args.output).write_text(json.dumps(evidence, ensure_ascii=False, indent=2), encoding="utf-8")
    with httpx.Client(base_url=args.go_url, timeout=70) as client:
        suffix = uuid4().hex[:12]
        registered = client.post("/api/v1/auth/register", json={"username":"opt_"+suffix,"email":suffix+"@example.test","password":"Local-fixture-password-42"})
        registered.raise_for_status()
        identity = registered.json()["data"]
        client.headers["Authorization"] = "Bearer " + identity["access_token"]
        checked("registration")
        fixture = Path(__file__).resolve().parents[1]/".artifacts"/"m13-fixtures"/"normal-text.pdf"
        with fixture.open("rb") as pdf:
            response = client.post("/api/v1/scripts/upload", data={"title":"Optimization storage acceptance"}, files={"file":("normal-text.pdf",pdf,"application/pdf")})
        response.raise_for_status()
        script = response.json()["data"]
        script_id = script["id"]
        checked("go_upload", script_id=script_id)
        objects = list(storage.list_objects(bucket, prefix=f"scripts/{identity['user_id']}/{script_id}/"))
        assert len(objects)==1
        storage.stat_object(bucket,objects[0].object_name)
        checked("minio_object_exists")
        deadline=time.monotonic()+300
        while time.monotonic()<deadline:
            detail=client.get(f"/api/v1/scripts/{script_id}").json()["data"]
            if detail["status"]=="ready":break
            if detail["status"]=="failed":raise RuntimeError("pipeline failed: "+detail.get("parse_error",""))
            time.sleep(2)
        else:raise RuntimeError("pipeline did not finish within 300 seconds")
        assert detail["chunk_count"]>0
        checked("python_pdf_bge_milvus_and_callback", chunk_count=detail["chunk_count"])
        from pymilvus import connections, Collection
        connections.connect(host=os.environ.get("TRPG_AI_MILVUS_HOST","127.0.0.1"), port=os.environ.get("TRPG_AI_MILVUS_PORT","39530"))
        collection=Collection(os.environ.get("TRPG_AI_MILVUS_COLLECTION_NAME","optimization_script_chunks"))
        collection.load()
        rows=collection.query(expr=f"script_id == {script_id}", output_fields=["script_id","content"])
        assert len(rows)==detail["chunk_count"]
        checked("milvus_index_count", indexed=len(rows))
        with httpx.Client(base_url=args.ai_url,timeout=10) as ai:
            assert ai.post("/api/v1/ai/memory/summary",json={"messages":[{"role":"assistant","content":"fact"}]}).status_code==401
            ready=ai.get("/ready")
            assert ready.status_code==503 and ready.json()["capabilities"]["ai"]=="missing_key"
            assert all(value=="ready" for value in ready.json()["dependencies"].values())
        checked("internal_auth_and_missing_key_readiness")
        deleted=client.delete(f"/api/v1/scripts/{script_id}");deleted.raise_for_status()
        try:storage.stat_object(bucket,objects[0].object_name)
        except S3Error as exc:assert exc.code=="NoSuchKey"
        else:raise AssertionError("object still exists")
        assert collection.query(expr=f"script_id == {script_id}",output_fields=["script_id"])==[]
        assert client.get(f"/api/v1/scripts/{script_id}").status_code==404
        checked("delete_object_vectors_and_business_record")

        # A real parser failure, followed by repairing only this disposable
        # object's bytes and exercising the public retry endpoint.
        blank = fixture.with_name("blank-scan.pdf")
        with blank.open("rb") as pdf:
            response = client.post("/api/v1/scripts/upload", data={"title":"Optimization retry acceptance"},
                files={"file":("blank-scan.pdf", pdf, "application/pdf")})
        response.raise_for_status()
        retry_id = response.json()["data"]["id"]
        def wait_status(expected):
            until = time.monotonic() + 300
            while time.monotonic() < until:
                result = client.get(f"/api/v1/scripts/{retry_id}")
                result.raise_for_status()
                detail = result.json()["data"]
                if detail["status"] == expected:
                    return detail
                time.sleep(2)
            raise RuntimeError("retry fixture did not reach " + expected)
        failed = wait_status("failed")
        assert failed["chunk_count"] == 0 and failed["parse_error"]
        checked("real_blank_pdf_failure_callback")
        repaired = list(storage.list_objects(bucket, prefix=f"scripts/{identity['user_id']}/{retry_id}/"))
        assert len(repaired) == 1
        with fixture.open("rb") as pdf:
            storage.put_object(bucket, repaired[0].object_name, pdf, fixture.stat().st_size, content_type="application/pdf")
        retry = client.post(f"/api/v1/scripts/{retry_id}/retry")
        retry.raise_for_status()
        ready = wait_status("ready")
        rows = collection.query(expr=f"script_id == {retry_id}", output_fields=["script_id"])
        assert len(rows) == ready["chunk_count"] > 0
        checked("repaired_pdf_public_retry_and_vector_count", chunk_count=ready["chunk_count"])
        client.delete(f"/api/v1/scripts/{retry_id}").raise_for_status()
        assert collection.query(expr=f"script_id == {retry_id}", output_fields=["script_id"]) == []
        checked("retry_fixture_cleanup")


if __name__=="__main__":main()
