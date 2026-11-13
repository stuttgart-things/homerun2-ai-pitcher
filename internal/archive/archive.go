// Package archive reads and writes the S3 buckets of the cluster analysis:
// the K8sGPT findings archived by homerun2-s3-catcher, and the reports.
package archive

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Options address one bucket.
type Options struct {
	Endpoint  string // host[:port]
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	Insecure  bool
}

// Object is one listing entry.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Bucket is one S3 bucket.
type Bucket struct {
	client *minio.Client
	name   string
}

// Open returns the bucket; it does not contact the server.
func Open(o Options) (*Bucket, error) {
	opts := &minio.Options{Creds: credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""), Secure: o.UseSSL, Region: o.Region}
	if o.Insecure {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via S3_INSECURE
		opts.Transport = tr
	}
	c, err := minio.New(o.Endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &Bucket{client: c, name: o.Bucket}, nil
}

// Prefixes returns the "directories" directly under prefix.
func (b *Bucket) Prefixes(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	for o := range b.client.ListObjects(ctx, b.name, minio.ListObjectsOptions{Prefix: prefix}) {
		if o.Err != nil {
			return nil, fmt.Errorf("list %s/%s: %w", b.name, prefix, o.Err)
		}
		if strings.HasSuffix(o.Key, "/") {
			out = append(out, o.Key)
		}
	}
	return out, nil
}

// List returns every object under prefix.
func (b *Bucket) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	for o := range b.client.ListObjects(ctx, b.name, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, fmt.Errorf("list %s/%s: %w", b.name, prefix, o.Err)
		}
		out = append(out, Object{Key: o.Key, Size: o.Size, LastModified: o.LastModified})
	}
	return out, nil
}

// maxObject bounds what Get reads.
const maxObject = 8 << 20

// Get reads one object.
func (b *Bucket) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := b.client.GetObject(ctx, b.name, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	defer func() { _ = obj.Close() }()
	data, err := io.ReadAll(io.LimitReader(obj, maxObject))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return data, nil
}

// Put writes one object.
func (b *Bucket) Put(ctx context.Context, key, contentType string, body []byte) error {
	_, err := b.client.PutObject(ctx, b.name, key, bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}
