// Package objstore 封装对象存储。仅依赖 S3 API，可对接 SeaweedFS（默认）、MinIO 或任意外部 S3。
package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Config 是 S3 连接参数。
type Config struct {
	Endpoint  string // 为空时使用 AWS 官方端点
	Region    string
	AccessKey string
	SecretKey string
	Bucket    string
	PathStyle bool
}

// Store 是对象存储客户端。
type Store struct {
	c      *s3.Client
	ps     *s3.PresignClient
	bucket string
}

// New 创建客户端。
func New(cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("objstore: bucket 不能为空")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	opts := s3.Options{
		Region:       cfg.Region,
		UsePathStyle: cfg.PathStyle,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		// 兼容非 AWS 实现：仅在必须时计算校验和
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
	}
	c := s3.New(opts)
	return &Store{c: c, ps: s3.NewPresignClient(c), bucket: cfg.Bucket}, nil
}

// EnsureBucket 若桶不存在则创建。
func (s *Store) EnsureBucket(ctx context.Context) error {
	if _, err := s.c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &s.bucket}); err == nil {
		return nil
	}
	_, err := s.c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &s.bucket})
	var owned *types.BucketAlreadyOwnedByYou
	var exists *types.BucketAlreadyExists
	if err != nil && !errors.As(err, &owned) && !errors.As(err, &exists) {
		return fmt.Errorf("create bucket: %w", err)
	}
	return nil
}

// Put 上传对象。
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.c.PutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key, Body: r,
		ContentLength: aws.Int64(size), ContentType: aws.String(contentType)})
	return err
}

// Get 下载对象，调用方负责关闭。
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.c.GetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// Delete 删除对象。
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key})
	return err
}

// List 列出前缀下的对象键。
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.c, &s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: &prefix})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// PresignGet 生成限时下载 URL。
func (s *Store) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	r, err := s.ps.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return r.URL, nil
}

// PresignPut 生成限时上传 URL。
func (s *Store) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	r, err := s.ps.PresignPutObject(ctx, &s3.PutObjectInput{Bucket: &s.bucket, Key: &key}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return r.URL, nil
}
