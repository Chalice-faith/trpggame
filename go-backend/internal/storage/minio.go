package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"trpggame/internal/config"
)

// MinIOStorage 管理剧本文件所在的 MinIO Bucket。
type MinIOStorage struct {
	client *minio.Client
	bucket string
}

// NewMinIOStorage 创建客户端，并确保配置的 Bucket 已存在。
func NewMinIOStorage(ctx context.Context, cfg *config.MinIOConfig) (*MinIOStorage, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	storage := &MinIOStorage{
		client: client,
		bucket: cfg.Bucket,
	}
	if err := storage.ensureBucket(ctx); err != nil {
		return nil, err
	}

	return storage, nil
}

// PutObject 上传对象，并返回 MinIO 实际写入的信息。
func (s *MinIOStorage) PutObject(
	ctx context.Context,
	objectName string,
	reader io.Reader,
	size int64,
	contentType string,
) (minio.UploadInfo, error) {
	info, err := s.client.PutObject(ctx, s.bucket, objectName, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return minio.UploadInfo{}, fmt.Errorf("put minio object %q: %w", objectName, err)
	}

	return info, nil
}

// RemoveObject 删除对象。MinIO 对不存在的对象也按成功处理，因此该操作可安全重试。
func (s *MinIOStorage) RemoveObject(ctx context.Context, objectName string) error {
	if err := s.client.RemoveObject(ctx, s.bucket, objectName, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("remove minio object %q: %w", objectName, err)
	}

	return nil
}

func (s *MinIOStorage) ensureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check minio bucket %q: %w", s.bucket, err)
	}
	if exists {
		return nil
	}

	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		return fmt.Errorf("create minio bucket %q: %w", s.bucket, err)
	}

	return nil
}
