package lksdk

import (
	"net/url"
	"time"

	"github.com/minio/minio-go"
)

func GeneratePreSignedUploadURL(accessKey, secret, endpoint, bucket, key string) (*url.URL, map[string]string, error) {
	policy := minio.NewPostPolicy()
	if err := policy.SetBucket(bucket); err != nil {
		return nil, nil, err
	}
	if err := policy.SetKey(key); err != nil {
		return nil, nil, err
	}
	if err := policy.SetExpires(time.Now().Add(time.Hour).UTC()); err != nil {
		return nil, nil, err
	}

	s3Client, err := minio.New(endpoint, accessKey, secret, true)
	if err != nil {
		return nil, nil, err
	}
	return s3Client.PresignedPostPolicy(policy)
}
