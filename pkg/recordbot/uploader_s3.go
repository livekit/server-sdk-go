package recordbot

import (
	"errors"
	"io"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials/stscreds"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type S3Config struct {
	Region string
	Bucket string
	ACL    string

	// We use Role IAM object attached to EC2 instance.
	RoleARN string
}

type s3Uploader struct {
	config S3Config
	client s3iface.S3API
}

var ErrEmptyS3BucketName = errors.New("empty S3 bucket name")
var ErrEmptyS3ACL = errors.New("empty S3 ACL")

func NewS3Uploader(config S3Config) (Uploader, error) {
	// Sanitise config
	if config.Bucket == "" {
		return nil, ErrEmptyS3BucketName
	} else if config.ACL == "" {
		return nil, ErrEmptyS3ACL
	}

	// Create S3 service instance
	sess := session.Must(session.NewSession(&aws.Config{
		Region: aws.String(config.Region),

		// For debugging purpose
		CredentialsChainVerboseErrors: aws.Bool(true),
	}))
	creds := stscreds.NewCredentials(sess, config.RoleARN)
	svc := s3.New(sess, &aws.Config{Credentials: creds})

	// Return success
	return &s3Uploader{config, svc}, nil
}

func (u *s3Uploader) Upload(key string, body io.Reader) error {
	// Instantiate new uploader
	awsUploader := s3manager.NewUploaderWithClient(u.client)

	// Upload to AWS
	_, err := awsUploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(u.config.Bucket),
		ACL:    aws.String(u.config.ACL),
		Key:    aws.String(key),
		Body:   body,
	})
	if err != nil {
		return err
	}

	// Return success
	return nil
}
