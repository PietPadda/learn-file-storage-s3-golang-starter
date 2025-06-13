// s3_service.go
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
)

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	// AWS SDK to create S3 Presign Client
	presignClient := s3.NewPresignClient(s3Client) // pass the client input
	// this the helper func that is used to send an http request for s3 files and signs in

	// params for presign Req
	params := &s3.GetObjectInput{
		Bucket: aws.String(bucket), // AWS needs *string, aws.String() does this for us
		Key:    aws.String(key),
	}
	// this struct provides the bucket and s3 file path (key) for the request

	// create presigned URL using getobject and expiration func WithPresignExpires for temp link
	presignReq, err := presignClient.PresignGetObject(
		context.Background(),              // no need for timeout, just background
		params,                            // bucket and s3 file path
		s3.WithPresignExpires(expireTime)) // url's expiration time
	// this is the special http request we make

	// presign URL request check
	if err != nil {
		return "", err // helper, pass error up to handler
	}

	// return the presigned URL generated from presignClient
	return presignReq.URL, nil
}

// videoURL in db update method
func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	// get videourl from input video
	videoURL := video.VideoURL // we have the bucket and key in here, comma-delimited

	// nil ptr check (safe deref)
	if videoURL == nil {
		return video, nil // early return (no uploaded video yet)
	}

	// split the videoURL by comma (splitN to limit # pieces)
	splitURL := strings.SplitN(*videoURL, ",", 2) // limit to 2 elements only
	// deref ptr to plain string

	// valid url check
	if len(splitURL) != 2 {
		return database.Video{}, fmt.Errorf("invalid videoURL: %s", *videoURL)
	} // deref ptr to plain string

	// get bucket and key
	bucket := splitURL[0]   // first part of URL
	key := splitURL[1]      // second part of URL
	expireTime := time.Hour // set expiration time (hour is reasonable)

	// generate a presign URL to update the videourl
	presignedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, expireTime)

	// gen presign URL check
	if err != nil {
		return database.Video{}, err // empty vid and err to handler
	}

	// update hte videoURL
	video.VideoURL = &presignedURL // address because it's *string

	// send the updated video to caller
	return video, nil
}
