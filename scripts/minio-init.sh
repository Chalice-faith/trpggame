#!/bin/sh
set -eu
# Credentials are passed only via environment; do not enable shell tracing.
attempt=0
until mc alias set storage http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 30 ] || { echo 'MinIO initialization connection failed' >&2; exit 1; }
  sleep 2
done
mc mb --ignore-existing "storage/$MINIO_BUCKET" >/dev/null
mc mb --ignore-existing storage/milvus-bucket >/dev/null
cat > /tmp/scripts-policy.json <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket"],"Resource":["arn:aws:s3:::$MINIO_BUCKET"]},{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject"],"Resource":["arn:aws:s3:::$MINIO_BUCKET/*"]}]}
EOF
cat > /tmp/milvus-policy.json <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::milvus-bucket","arn:aws:s3:::milvus-bucket/*"]}]}
EOF
mc admin policy create storage trpg-scripts /tmp/scripts-policy.json >/dev/null
mc admin policy create storage trpg-milvus /tmp/milvus-policy.json >/dev/null
mc admin user add storage "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null
mc admin user add storage "$MILVUS_ACCESS_KEY" "$MILVUS_SECRET_KEY" >/dev/null
mc admin policy attach storage trpg-scripts --user "$MINIO_ACCESS_KEY" >/dev/null
mc admin policy attach storage trpg-milvus --user "$MILVUS_ACCESS_KEY" >/dev/null
echo 'MinIO business users and bucket policies initialized'
