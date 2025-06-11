package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

// Structs
type FFProbeOutput struct {
	Streams []FFProbeStream `json:"streams"`
}

type FFProbeStream struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

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

	// only allow mp4
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

	// get temp file path (process video before getting aspect ratio)
	tempFilePath := tempFile.Name() // .Name() gets the /tmp/filename.ext file path

	// process video for fast start
	processedFilePath, err := processVideoForFastStart(tempFilePath)

	// process check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing video for fast start", err)
		return // early return
	}
	defer os.Remove(processedFilePath) // clean up after to prevent mem leak

	// open the processed file (for S3 upload & AR get)
	processedFile, err := os.Open(processedFilePath)

	// open processed file check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error opening processed file", err)
		return // early return
	}
	defer processedFile.Close() // prevent mem leak

	// get aspect ratio (from processed file)
	aspectRatio, err := getVideoAspectRatio(processedFilePath) // pass tmp filepath to helper

	// aspect ratio check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error getting video aspect ratio", err)
		return // early return
	}

	// determine aspect ratio prefix (init before to enter switch scope)
	var aspectRatioPrefix string

	// check the cases and set the fileKey prefix
	switch aspectRatio {
	case "16:9":
		aspectRatioPrefix = "landscape/"
	case "9:16":
		aspectRatioPrefix = "portrait/"
	default:
		aspectRatioPrefix = "other/"
	}
	// encode to HEX for URL safety (AWS S3 favours this)
	hexString := hex.EncodeToString(randomBytes)

	// build the fileKey
	fileKey := aspectRatioPrefix + hexString + fileExt // this will be the AWS string for filename

	// create S3 put object parameters
	putParams := &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),     // bucket from .env
		Key:         aws.String(fileKey),          // oue new filename
		Body:        processedFile,                // io.Reader
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

// HELPER FUNCTIONS
func getVideoAspectRatio(filePath string) (string, error) {
	// execute ffprobe command
	cmd := exec.Command(
		"ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		filePath,
	)

	// direct output to bytes.Buffer
	var out bytes.Buffer // hold cmd output
	cmd.Stdout = &out    // store cmd output to this in-memory byte slice

	// run the cmd
	err := cmd.Run() // out.Bytes() contains the stdout of ffprobe
	// this "runs" the cmd, with output in the buffer, ready for parsing etc

	// run check
	if err != nil {
		return "", err // error is returned upwards ie to handler
	}

	// create zero slice for data response
	var ffProbeOutput FFProbeOutput // entire output, incl internal structs

	// get output as []byte] slice for unmarshal
	outBytes := out.Bytes()

	// unmarshal to conv from raw json to go readable code
	err = json.Unmarshal(outBytes, &ffProbeOutput)

	// unmarshal check
	if err != nil {
		return "", fmt.Errorf("error unmarshalling json data: %w", err) // nil slice & error
	}

	// get video aspect ratio (assume it's first stream)
	videoWidth := ffProbeOutput.Streams[0].Width
	videoHeight := ffProbeOutput.Streams[0].Height

	// calculate ratio using float64 (optimal for 64bit, and Go std)
	aspectRatio := float64(videoWidth) / float64(videoHeight)

	// check if 16:9 (~1.78)
	if aspectRatio > 1.7 && aspectRatio < 1.85 {
		return "16:9", nil
		// check if 9:16 (~0.56)
	} else if aspectRatio > 0.52 && aspectRatio < 0.6 {
		return "9:16", nil
		// otherwise other
	} else {
		return "other", nil
	}
}

func processVideoForFastStart(filePath string) (string, error) {
	// output file path
	outFilePath := filePath + ".processing"

	// execute ffmpeg command
	cmd := exec.Command(
		"ffmpeg",       // run ffmpeg app
		"-i", filePath, // input file path
		"-c", "copy", // copy no encode
		"-movflags", "faststart", // move moov atom to start
		"-f", "mp4", // output format mp4
		outFilePath, // processed output filepath
	)

	// direct output to bytes.Buffer
	var out bytes.Buffer // hold cmd output
	cmd.Stdout = &out    // store cmd output to this in-memory byte slice

	// capture ffmpeg error
	var stderr bytes.Buffer // hold cmd error
	cmd.Stderr = &stderr    // store cmd error to this in-memory byte slice

	// run the cmd
	err := cmd.Run() // out.Bytes() contains the stdout of ffmpeg
	// this "runs" the cmd, with output in the buffer, ready for parsing etc

	// run check
	if err != nil {
		return "", fmt.Errorf("ffmpeg error: %v, details: %s", err, stderr.String()) // error is returned upwards ie to handler
	}

	// output the processed filepath
	return outFilePath, nil
}
