/******************************************************************************
 *
 *  Description :
 *
 *    Handler of large file uploads/downloads. Validates request first then calls
 *    a handler.
 *
 *****************************************************************************/

package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/tinode/chat/server/store"
	"github.com/tinode/chat/server/store/types"
)

func largeFileServe(wrt http.ResponseWriter, req *http.Request) {
	now := time.Now().UTC().Round(time.Millisecond)
	enc := json.NewEncoder(wrt)
	mh := store.GetMediaHandler()

	//Check if media handler requests redirection to another service.
	if redirTo := mh.Redirect(req.URL.String()); redirTo != "" {
		wrt.Header().Set("Location", redirTo)
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(http.StatusFound)
		enc.Encode(InfoFound("", "", now))
		log.Println("media serve redirected", redirTo)
		return
	}

	writeHttpResponse := func(msg *ServerComMessage, err error) {
		// Gorilla CompressHandler requires Content-Type to be set.
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(msg.Ctrl.Code)
		enc.Encode(msg)

		log.Println("media serve", msg.Ctrl.Code, msg.Ctrl.Text, err)
	}

	// Check for API key presence
	if isValid, _ := checkAPIKey(getAPIKey(req)); !isValid {
		writeHttpResponse(ErrAPIKeyRequired(now), nil)
		return
	}

	// Check authorization: either auth information or SID must be present
	uid, challenge, err := authHttpRequest(req)
	if err != nil {
		writeHttpResponse(decodeStoreError(err, "", "", now, nil), err)
		return
	}
	if challenge != nil {
		writeHttpResponse(InfoChallenge("", now, challenge), nil)
		return
	}
	if uid.IsZero() {
		// Not authenticated
		writeHttpResponse(ErrAuthRequired("", "", now), nil)
		return
	}

	fd, rsc, err := mh.Download(req.URL.String())
	if err != nil {
		writeHttpResponse(decodeStoreError(err, "", "", now, nil), err)
		return
	}

	defer rsc.Close()

	wrt.Header().Set("Content-Type", fd.MimeType)
	wrt.Header().Set("Content-Disposition", "attachment")
	http.ServeContent(wrt, req, "", fd.UpdatedAt, rsc)

	log.Println("media served OK")
}

// largeFileUpload receives files from client over HTTP(S) and saves them to local file
// system.
func largeFileUpload(wrt http.ResponseWriter, req *http.Request) {
	now := time.Now().UTC().Round(time.Millisecond)
	enc := json.NewEncoder(wrt)
	mh := store.GetMediaHandler()

	// Check if uploads are handled elsewhere.
	if redirTo := mh.Redirect(req.URL.String()); redirTo != "" {
		wrt.Header().Set("Location", redirTo)
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(http.StatusFound)
		enc.Encode(InfoFound("", "", now))

		log.Println("media upload redirected", redirTo)
		return
	}

	writeHttpResponse := func(msg *ServerComMessage, err error) {
		// Gorilla CompressHandler requires Content-Type to be set.
		wrt.Header().Set("Content-Type", "application/json; charset=utf-8")
		wrt.WriteHeader(msg.Ctrl.Code)
		enc.Encode(msg)

		log.Println("media upload", msg.Ctrl.Code, msg.Ctrl.Text, err)
	}

	// Check if this is a POST request
	if req.Method != http.MethodPost {
		writeHttpResponse(ErrOperationNotAllowed("", "", now), nil)
		return
	}

	if globals.maxFileUploadSize > 0 {
		// Enforce maximum upload size.
		req.Body = http.MaxBytesReader(wrt, req.Body, globals.maxFileUploadSize)
	}

	// Check for API key presence
	if isValid, _ := checkAPIKey(getAPIKey(req)); !isValid {
		writeHttpResponse(ErrAPIKeyRequired(now), nil)
		return
	}

	msgID := req.FormValue("id")
	// Check authorization: either auth information or SID must be present
	uid, challenge, err := authHttpRequest(req)
	if err != nil {
		writeHttpResponse(decodeStoreError(err, msgID, "", now, nil), err)
		return
	}
	if challenge != nil {
		writeHttpResponse(InfoChallenge(msgID, now, challenge), nil)
		return
	}
	if uid.IsZero() {
		// Not authenticated
		writeHttpResponse(ErrAuthRequired(msgID, "", now), nil)
		return
	}

	file, _, err := req.FormFile("file")
	if err != nil {
		if strings.Contains(err.Error(), "request body too large") {
			writeHttpResponse(ErrTooLarge(msgID, "", now), err)
		} else {
			writeHttpResponse(ErrMalformed(msgID, "", now), err)
		}
		return
	}
	fdef := types.FileDef{}
	fdef.Id = store.GetUidString()
	fdef.InitTimes()
	fdef.User = uid.String()

	buff := make([]byte, 512)
	if _, err = file.Read(buff); err != nil {
		writeHttpResponse(ErrUnknown(msgID, "", now), err)
		return
	}

	fdef.MimeType = http.DetectContentType(buff)
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		writeHttpResponse(ErrUnknown(msgID, "", now), err)
		return
	}

	url, err := mh.Upload(&fdef, file)
	if err != nil {
		writeHttpResponse(decodeStoreError(err, msgID, "", now, nil), err)
		return
	}

	resp := NoErr(msgID, "", now)
	resp.Ctrl.Params = map[string]string{"url": url}
	writeHttpResponse(resp, nil)
}

func largeFileRunGarbageCollection(period time.Duration, block int) chan<- bool {
	stop := make(chan bool)
	go func() {
		gcTimer := time.Tick(period)
		for {
			select {
			case <-gcTimer:
				if err := store.Files.DeleteUnused(time.Now().Add(-time.Hour), block); err != nil {
					log.Println("media gc:", err)
				}
			case <-stop:
				return
			}
		}
	}()

	return stop
}
