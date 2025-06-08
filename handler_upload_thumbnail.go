package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// parse multipart data
	const maxMemory = 10 << 20      // 10 * 2^10 * 2^10 = 10mb, max memory, rest goes to temp disk storage
	r.ParseMultipartForm(maxMemory) // we decode (parse) file with maxMemory storage set

	file, fileHeader, err := r.FormFile("thumbnail") // get thumbnail, the type and error

	// form file get check
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse multipart form file", err)
		return // early return
	}
	defer file.Close() // stop reading when done, prevent mem leak

	// get the thumbnail media type
	mediaContentType := fileHeader.Header.Get("Content-Type") // jpg, png, gif etc from fileHeader

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
	case "image/jpeg": // if jpeg
		fileExt = ".jpg" // set ext accordingly
	case "image/png": // if png
		fileExt = ".png" // set ext accordingly
	default: // if ANYTHING else
		errorMessage := fmt.Sprintf("Invalid thumbnail type: %s", mediaType) // custom msg
		respondWithError(w, http.StatusBadRequest, errorMessage, nil)        // nil, not an error
		return                                                               // early return
	}

	// get video metadata from db
	video, err := cfg.db.GetVideo(videoID)

	// get video check
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return // early return
	}

	// authorisation check using apiConfig
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You can't upload a thumbnail for this video", nil) // no sys err
		return                                                                                           // early return
	}

	// generate random 32-byte slice for filename
	randomBytes := make([]byte, 32) // init a slice
	rand.Read(randomBytes)          // generate here
	// no err as Read ALWAYS succeed

	// RawURLEncode for clean and file-safe filename using base64 str
	randomName := base64.RawURLEncoding.EncodeToString(randomBytes) // random)

	// build filesystem path for thumbnail
	filePath := filepath.Join(cfg.assetsRoot, randomName+fileExt) // use base64 random name
	// ./assets/randomName.ext as the unique path!

	// create empty output file
	outFile, err := os.Create(filePath)

	// empty file check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error creating file", err)
		return // early return
	}

	// copy imageData contents to this new empty file
	_, err = io.Copy(outFile, file) // write filedata to outFile

	// io.Copy check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying thumbnail data to file", err)
		return // early return
	}

	// close file and outFile os/io reading on func end, prevent mem leak
	defer outFile.Close()

	// build the thumbnail path (filesystem)
	thumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s%s", cfg.port, randomName, fileExt) // path of file on filesystem

	// update the video thumbnail DATA url path
	video.ThumbnailURL = &thumbnailURL // note it's a pointer field (write to field)
	updatedVideo := cfg.db.UpdateVideo(video)

	// respond to client with the updated video struct
	respondWithJSON(w, http.StatusOK, updatedVideo)
}
