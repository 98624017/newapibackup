package backup

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakeS3Client struct {
	bucket      string
	key         string
	contentType string
	body        string
}

func (f *fakeS3Client) PutObject(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.bucket = aws.ToString(input.Bucket)
	f.key = aws.ToString(input.Key)
	f.contentType = aws.ToString(input.ContentType)
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	f.body = string(data)
	return &s3.PutObjectOutput{}, nil
}

func TestNewS3UploaderPutsObject(t *testing.T) {
	tmp := t.TempDir()
	path := tmp + "/backup.sql.gz"
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeS3Client{}
	uploader := NewS3Uploader(client, "bucket")

	if err := uploader(context.Background(), "newapi/full/latest.json", path, "application/json"); err != nil {
		t.Fatalf("upload returned error: %v", err)
	}

	if client.bucket != "bucket" {
		t.Fatalf("bucket = %q, want bucket", client.bucket)
	}
	if client.key != "newapi/full/latest.json" {
		t.Fatalf("key = %q, want latest key", client.key)
	}
	if client.contentType != "application/json" {
		t.Fatalf("contentType = %q, want application/json", client.contentType)
	}
	if client.body != "backup" {
		t.Fatalf("body = %q, want backup", client.body)
	}
}
