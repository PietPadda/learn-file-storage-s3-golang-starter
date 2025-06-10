package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID") // for extracting videoID from URL path

	// parse the string as a UUID
	videoID, err := uuid.Parse(videoIDString)

	// uuid parse check
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// get JWT token
	token, err := auth.GetBearerToken(r.Header)

	// missing JWT check
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	// validate user with JWT
	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)

	// validation check
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// server log
	fmt.Println("uploading video", videoID, "by user", userID)

	// get video metadata from db
	video, err := cfg.db.GetVideo(videoID)

	// get video check
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return // early return
	}

	// authorisation check using apiConfig
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You can't upload a video for this user", nil) // no sys err
		return                                                                                      // early return
	}

	// set max video upload size
	const maxUploadSize = 1 << 30 // 1 * 2^30 = 1gb, max size
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	// we decode (parse) file with max upload size set
	err = r.ParseMultipartForm(maxUploadSize)

	// form file get check
	if err != nil {
		// first check if file size error
		// store variable for error type
		var maxBytesErr *http.MaxBytesError // wrap body and enforce size limit
		if errors.As(err, &maxBytesErr) {   // check if error marches
			respondWithError(w, http.StatusRequestEntityTooLarge, "File is too large", err)
			return // early return
		}

		// continue with general error check
		respondWithError(w, http.StatusBadRequest, "Unable to parse form", err)
		return // early return
	}

	// get video, the type and error
	file, fileHeader, err := r.FormFile("video")

	// form file get check
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse multipart form file", err)
		return // early return
	}
	defer file.Close() // stop reading when done, prevent mem leak

	// get the video media type
	mediaContentType := fileHeader.Header.Get("Content-Type") // mp4 etc from fileHeader

	// get MIME extension via parsing
	mediaType, _, err := mime.ParseMediaType(mediaContentType) // pass in header content type, ignore params

	// mime extension check
	if err != nil {
		errorMessage := fmt.Sprintf("Error processing extension %s: %v", mediaType, err) // custom msg for 2 vars
		respondWithError(w, http.StatusInternalServerError, errorMessage, err)           // add to single var msg
		return                                                                           // early return
	}

	var fileExt string // init string (for scoping)

	// only allow jpeg and png
	switch mediaType {
	case "video/mp4": // if mp4
		fileExt = ".mp4" // set ext accordingly
	default: // if ANYTHING else
		errorMessage := fmt.Sprintf("Invalid video type: %s", mediaType) // custom msg
		respondWithError(w, http.StatusBadRequest, errorMessage, nil)    // nil, not an error
		return                                                           // early return
	}

	// save uploaded file to temp file on disk
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	// "" path = system default path, and provide a template name for the file

	// create temp check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating temp file", err)
		return // early return
	}

	// defer closing and removing the file (remember defer LIFO)
	defer func() {
		tempFile.Close()           // first close
		os.Remove(tempFile.Name()) // remove last
	}()
	// if separate defer lines, remove BEFORE close, otherwise remove will run first! LIFO defer!

	// copy contents for wire (http req) to temp file
	_, err = io.Copy(tempFile, file) // write filedata to tempFile

	// io.Copy check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying video data to file", err)
		return // early return
	}

	// reset tempFile pointer to start (after io.Copy)
	_, err = tempFile.Seek(0, io.SeekStart)
	// set position 0, relative from file start

	// reset ptr check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error resetting temp file pointer", err)
		return // early return
	}

	// generate random 32-byte slice for filename
	randomBytes := make([]byte, 32) // init a slice
	rand.Read(randomBytes)          // generate here
	// no err as Read ALWAYS succeed (crypto)

	// encode to HEX for URL safety (AWS S3 favours this)
	hexString := hex.EncodeToString(randomBytes)
	fileKey := hexString + fileExt // this will be the AWS string for filename

	// create S3 put object parameters
	putParams := &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),     // bucket from .env
		Key:         aws.String(fileKey),          // oue new filename
		Body:        tempFile,                     // io.Reader
		ContentType: aws.String(mediaContentType), // extension
	}
	// aws.String() cnvrts string to *string - AWS needs pointers to omit fields by passing nil

	// UPLOAD (put) the VIDEO (object) into S3 (SERVERLESS STORAGE BUCKET)
	_, err = cfg.s3Client.PutObject(context.Background(), putParams)

	// put check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading video to S3", err)
		return // early return
	}

	// build the VideoURL (Amazon S3)
	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, fileKey) // path of file on S3

	// update the video thumbnail DATA url path
	video.VideoURL = &videoURL                // note it's a pointer field (write to field)
	updatedVideo := cfg.db.UpdateVideo(video) // update our DB VideoURL with S3 path
	// NOTE: UpdateVideo doesn't return err

	// respond to client with the updated video struct
	respondWithJSON(w, http.StatusOK, updatedVideo)
}
