package cmd

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"os"
	"testing"
)

func TestMakeS3Client(t *testing.T) {
	os.Setenv("BUCKET_NAME", "foo")
	c := MakeS3Client(aws.Config{})
	assert.NotNil(t, c)
	assert.Equal(t, "foo", c.BucketName)
}

func TestPutObject(t *testing.T) {
	mockS3 := &MockObjectStore{}
	mockS3.On("PutObject", mock.Anything, mock.Anything).
		Return(&s3.PutObjectOutput{}, nil)

	c := S3Client{
		BucketName: "foo",
		client:     mockS3,
	}
	_, err := c.PutObject(context.TODO(), &s3.PutObjectInput{})
	assert.Nil(t, err)
}

func TestDeleteObject(t *testing.T) {
	mockS3 := &MockObjectStore{}
	mockS3.On("DeleteObject", mock.Anything, mock.Anything).
		Return(&s3.DeleteObjectOutput{}, nil)

	c := S3Client{
		BucketName: "foo",
		client:     mockS3,
	}
	_, err := c.DeleteObject(context.TODO(), &s3.DeleteObjectInput{})
	assert.Nil(t, err)
}

func TestListObjects(t *testing.T) {
	mockS3 := &MockObjectStore{}
	mockS3.On("ListObjectsV2", mock.Anything, mock.Anything).
		Return(&s3.ListObjectsV2Output{}, nil)

	c := S3Client{
		BucketName: "foo",
		client:     mockS3,
	}
	_, err := c.ListObjectsV2(context.TODO(), &s3.ListObjectsV2Input{})
	assert.Nil(t, err)
}
