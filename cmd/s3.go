package cmd

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"os"
)

type S3Client struct {
	BucketName string
	client     ObjectStore
}

type ObjectStore interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// MakeS3Client Creates a new S3 Client object.
func MakeS3Client(cfg aws.Config) *S3Client {
	return &S3Client{
		BucketName: os.Getenv("BUCKET_NAME"),
		client:     s3.NewFromConfig(cfg),
	}
}

func (s *S3Client) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	return s.client.PutObject(ctx, params, optFns...)
}
