package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"

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

	// read into byte slice (images are binary data)
	imageData, err := io.ReadAll(file) // read data from the pipe (decoded data)

	// pipe read check
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error reading file", err)
		return // early return
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

	// base64 encode the image data to store as text in db :)
	base64String := base64.StdEncoding.EncodeToString(imageData)

	// build the thumbnail path
	thumbnailURL := fmt.Sprintf("data:%s;base64,%s", mediaType, base64String) // data URL with type and text encoded image

	// update the video thumbnail DATA url path
	video.ThumbnailURL = &thumbnailURL // note it's a pointer field (write to field)
	updatedVideo := cfg.db.UpdateVideo(video)

	// respond to client with the updated video struct
	respondWithJSON(w, http.StatusOK, updatedVideo)
}
