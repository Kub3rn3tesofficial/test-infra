package awsapi

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"io"
)

func S3Put(reader io.Reader, handle *BucketHandle, key string) error {
	uploader := s3manager.NewUploader(handle.client.session)

	_, err := uploader.Upload(&s3manager.UploadInput{
		Body:   reader,
		Bucket: aws.String(handle.bucket),
		Key:    aws.String(key),
	})

		return err


/*	target, err := os.Create("/Users/Traiana/alexa/Downloads/fw.txt")
	defer target.Close()

	w := bufio.NewWriter(target)
	io.Copy(w, reader)
	w.Flush()
	return err*/
}