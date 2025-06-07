package main

import (
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
	mediaType := fileHeader.Header.Get("Content-Type") // jpg, png, gif etc from fileHeader

	// get MIME extension
	exts, err := mime.ExtensionsByType(mediaType)

	// mime extension check
	if err != nil {
		errorMessage := fmt.Sprintf("Error processing extension %s: %v", mediaType, err) // custom msg for 2 vars
		respondWithError(w, http.StatusInternalServerError, errorMessage, err)           // add to single var msg
		return                                                                           // early return
	}

	var fileExt string // init string

	// check if exts valid
	if len(exts) > 0 {
		fileExt = exts[0] // get most common ext type (first in slice)
	} else { // otherwise, it's empty
		errorMessage := fmt.Sprintf("Unsupported media type or no extension found for %s", mediaType) // custom msg
		respondWithError(w, http.StatusBadRequest, errorMessage, nil)                                 // no err, but bad req
		return                                                                                        // early return
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

	// build filesystem path for thumbnail
	filePath := filepath.Join(cfg.assetsRoot, videoID.String()+fileExt) // videoID is uuid, need str
	// ./assets/videoID.ext as the unique path!

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
	thumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s%s", cfg.port, videoID, fileExt) // path of file on filesystem

	// update the video thumbnail DATA url path
	video.ThumbnailURL = &thumbnailURL // note it's a pointer field (write to field)
	updatedVideo := cfg.db.UpdateVideo(video)

	// respond to client with the updated video struct
	respondWithJSON(w, http.StatusOK, updatedVideo)
}
