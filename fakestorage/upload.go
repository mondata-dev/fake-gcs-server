// Copyright 2017 Francisco Souza. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fakestorage

import (
	"crypto/md5" // #nosec G501
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"io/ioutil"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/gorilla/mux"
)

const contentTypeHeader = "Content-Type"

const (
	uploadTypeMedia     = "media"
	uploadTypeMultipart = "multipart"
	uploadTypeResumable = "resumable"
)

type multipartMetadata struct {
	ContentType     string            `json:"contentType"`
	ContentEncoding string            `json:"contentEncoding"`
	Name            string            `json:"name"`
	Metadata        map[string]string `json:"metadata"`
}

type contentRange struct {
	KnownRange bool // Is the range known, or "*"?
	KnownTotal bool // Is the total known, or "*"?
	Start      int  // Start of the range, -1 if unknown
	End        int  // End of the range, -1 if unknown
	Total      int  // Total bytes expected, -1 if unknown
}

func (s *Server) insertObject(r *http.Request) jsonResponse {
	bucketName := mux.Vars(r)["bucketName"]

	if _, err := s.backend.GetBucket(bucketName); err != nil {
		return jsonResponse{status: http.StatusNotFound}
	}
	uploadType := r.URL.Query().Get("uploadType")
	if uploadType == "" && r.Header.Get("X-Goog-Upload-Protocol") == uploadTypeResumable {
		uploadType = uploadTypeResumable
	}

	switch uploadType {
	case uploadTypeMedia:
		return s.simpleUpload(bucketName, r)
	case uploadTypeMultipart:
		return s.multipartUpload(bucketName, r)
	case uploadTypeResumable:
		return s.resumableUpload(bucketName, r)
	default:
		// Support Signed URL Uploads
		if r.URL.Query().Get("X-Goog-Algorithm") != "" {
			switch r.Method {
			case http.MethodPost:
				return s.resumableUpload(bucketName, r)
			case http.MethodPut:
				return s.signedUpload(bucketName, r)
			}
		}
		return jsonResponse{errorMessage: "invalid uploadType", status: http.StatusBadRequest}
	}
}

func (s *Server) checkUploadPreconditions(r *http.Request, bucketName string, objectName string) *jsonResponse {
	ifGenerationMatch := r.URL.Query().Get("ifGenerationMatch")

	if ifGenerationMatch == "0" {
		if _, err := s.backend.GetObject(bucketName, objectName); err == nil {
			return &jsonResponse{
				status:       http.StatusPreconditionFailed,
				errorMessage: "Precondition failed",
			}
		}
	} else if ifGenerationMatch != "" || r.URL.Query().Get("ifGenerationNotMatch") != "" {
		return &jsonResponse{
			status:       http.StatusNotImplemented,
			errorMessage: "Precondition support not implemented",
		}
	}

	return nil
}

func (s *Server) simpleUpload(bucketName string, r *http.Request) jsonResponse {
	defer r.Body.Close()
	name := r.URL.Query().Get("name")
	predefinedACL := r.URL.Query().Get("predefinedAcl")
	contentEncoding := r.URL.Query().Get("contentEncoding")
	if name == "" {
		return jsonResponse{
			status:       http.StatusBadRequest,
			errorMessage: "name is required for simple uploads",
		}
	}
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	obj := Object{
		BucketName:      bucketName,
		Name:            name,
		Content:         data,
		ContentType:     r.Header.Get(contentTypeHeader),
		ContentEncoding: contentEncoding,
		Crc32c:          encodedCrc32cChecksum(data),
		Md5Hash:         encodedMd5Hash(data),
		ACL:             getObjectACL(predefinedACL),
	}
	obj, err = s.createObject(obj)
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	return jsonResponse{data: obj}
}

func (s *Server) signedUpload(bucketName string, r *http.Request) jsonResponse {
	defer r.Body.Close()
	name := mux.Vars(r)["objectName"]
	predefinedACL := r.URL.Query().Get("predefinedAcl")
	contentEncoding := r.URL.Query().Get("contentEncoding")

	// Load data from HTTP Headers
	if contentEncoding == "" {
		contentEncoding = r.Header.Get("Content-Encoding")
	}

	metaData := make(map[string]string)
	for key := range r.Header {
		lowerKey := strings.ToLower(key)
		if metaDataKey := strings.TrimPrefix(lowerKey, "x-goog-meta-"); metaDataKey != lowerKey {
			metaData[metaDataKey] = r.Header.Get(key)
		}
	}

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	obj := Object{
		BucketName:      bucketName,
		Name:            name,
		Content:         data,
		ContentType:     r.Header.Get(contentTypeHeader),
		ContentEncoding: contentEncoding,
		Crc32c:          encodedCrc32cChecksum(data),
		Md5Hash:         encodedMd5Hash(data),
		ACL:             getObjectACL(predefinedACL),
		Metadata:        metaData,
	}
	obj, err = s.createObject(obj)
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	return jsonResponse{data: obj}
}

func getObjectACL(predefinedACL string) []storage.ACLRule {
	if predefinedACL == "publicRead" {
		return []storage.ACLRule{
			{
				Entity: "allUsers",
				Role:   "READER",
			},
		}
	}

	return []storage.ACLRule{
		{
			Entity: "projectOwner",
			Role:   "OWNER",
		},
	}
}

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

func crc32cChecksum(content []byte) []byte {
	checksummer := crc32.New(crc32cTable)
	checksummer.Write(content)
	return checksummer.Sum(make([]byte, 0, 4))
}

func encodedChecksum(checksum []byte) string {
	return base64.StdEncoding.EncodeToString(checksum)
}

func encodedCrc32cChecksum(content []byte) string {
	return encodedChecksum(crc32cChecksum(content))
}

func md5Hash(b []byte) []byte {
	/* #nosec G401 */
	h := md5.New()
	h.Write(b)
	return h.Sum(nil)
}

func encodedHash(hash []byte) string {
	return base64.StdEncoding.EncodeToString(hash)
}

func encodedMd5Hash(content []byte) string {
	return encodedHash(md5Hash(content))
}

func (s *Server) multipartUpload(bucketName string, r *http.Request) jsonResponse {
	defer r.Body.Close()
	_, params, err := mime.ParseMediaType(r.Header.Get(contentTypeHeader))
	if err != nil {
		return jsonResponse{
			status:       http.StatusBadRequest,
			errorMessage: "invalid Content-Type header",
		}
	}
	var (
		metadata *multipartMetadata
		content  []byte
	)
	var contentType string
	reader := multipart.NewReader(r.Body, params["boundary"])
	part, err := reader.NextPart()
	for ; err == nil; part, err = reader.NextPart() {
		if metadata == nil {
			metadata, err = loadMetadata(part)
			contentType = metadata.ContentType
		} else {
			contentType = part.Header.Get(contentTypeHeader)
			content, err = loadContent(part)
		}
		if err != nil {
			break
		}
	}
	if err != io.EOF {
		return jsonResponse{errorMessage: err.Error()}
	}

	objName := r.URL.Query().Get("name")
	predefinedACL := r.URL.Query().Get("predefinedAcl")
	if objName == "" {
		objName = metadata.Name
	}

	if resp := s.checkUploadPreconditions(r, bucketName, objName); resp != nil {
		return *resp
	}

	obj := Object{
		BucketName:      bucketName,
		Name:            objName,
		Content:         content,
		ContentType:     contentType,
		ContentEncoding: metadata.ContentEncoding,
		Crc32c:          encodedCrc32cChecksum(content),
		Md5Hash:         encodedMd5Hash(content),
		ACL:             getObjectACL(predefinedACL),
		Metadata:        metadata.Metadata,
	}
	obj, err = s.createObject(obj)
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	return jsonResponse{data: obj}
}

func (s *Server) resumableUpload(bucketName string, r *http.Request) jsonResponse {
	predefinedACL := r.URL.Query().Get("predefinedAcl")
	contentEncoding := r.URL.Query().Get("contentEncoding")
	metadata, err := loadMetadata(r.Body)
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	objName := r.URL.Query().Get("name")
	if objName == "" {
		objName = metadata.Name
	}
	obj := Object{
		BucketName:      bucketName,
		Name:            objName,
		ContentEncoding: contentEncoding,
		ACL:             getObjectACL(predefinedACL),
		Metadata:        metadata.Metadata,
	}
	uploadID, err := generateUploadID()
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	s.uploads.Store(uploadID, obj)
	header := make(http.Header)
	header.Set("Location", s.URL()+"/upload/resumable/"+uploadID)
	if r.Header.Get("X-Goog-Upload-Command") == "start" {
		header.Set("X-Goog-Upload-URL", s.URL()+"/upload/resumable/"+uploadID)
		header.Set("X-Goog-Upload-Status", "active")
	}
	return jsonResponse{
		data:   obj,
		header: header,
	}
}

// uploadFileContent accepts a chunk of a resumable upload
//
// A resumable upload is sent in one or more chunks. The request's
// "Content-Range" header is used to determine if more data is expected.
//
// When sending streaming content, the total size is unknown until the stream
// is exhausted. The Go client always sends streaming content. The sequence of
// "Content-Range" headers for 2600-byte content sent in 1000-byte chunks are:
//
//   Content-Range: bytes 0-999/*
//   Content-Range: bytes 1000-1999/*
//   Content-Range: bytes 2000-2599/*
//   Content-Range: bytes */2600
//
// When sending chunked content of a known size, the total size is sent as
// well. The Python client uses this method to upload files and in-memory
// content. The sequence of "Content-Range" headers for the 2600-byte content
// sent in 1000-byte chunks are:
//
//   Content-Range: bytes 0-999/2600
//   Content-Range: bytes 1000-1999/2600
//   Content-Range: bytes 2000-2599/2600
//
// The server collects the content, analyzes the "Content-Range", and returns a
// "308 Permanent Redirect" response if more chunks are expected, and a
// "200 OK" response if the upload is complete (the Go client also accepts a
// "201 Created" response). The "Range" header in the response should be set to
// the size of the content received so far, such as:
//
//   Range: bytes 0-2000
//
// The client (such as the Go client) can send a header "X-Guploader-No-308" if
// it can't process a native "308 Permanent Redirect". The in-process response
// then has a status of "200 OK", with a header "X-Http-Status-Code-Override"
// set to "308".
func (s *Server) uploadFileContent(r *http.Request) jsonResponse {
	uploadID := mux.Vars(r)["uploadId"]
	rawObj, ok := s.uploads.Load(uploadID)
	if !ok {
		return jsonResponse{status: http.StatusNotFound}
	}
	obj := rawObj.(Object)
	content, err := loadContent(r.Body)
	if err != nil {
		return jsonResponse{errorMessage: err.Error()}
	}
	commit := true
	status := http.StatusOK
	obj.Content = append(obj.Content, content...)
	obj.Crc32c = encodedCrc32cChecksum(obj.Content)
	obj.Md5Hash = encodedMd5Hash(obj.Content)
	obj.ContentType = r.Header.Get(contentTypeHeader)
	responseHeader := make(http.Header)
	if contentRange := r.Header.Get("Content-Range"); contentRange != "" {
		parsed, err := parseContentRange(contentRange)
		if err != nil {
			return jsonResponse{errorMessage: err.Error(), status: http.StatusBadRequest}
		}
		if parsed.KnownRange {
			// Middle of streaming request, or any part of chunked request
			responseHeader.Set("Range", fmt.Sprintf("bytes=0-%d", parsed.End))
			// Complete if the range covers the known total
			commit = parsed.KnownTotal && (parsed.End+1 >= parsed.Total)
		} else {
			// End of a streaming request
			responseHeader.Set("Range", fmt.Sprintf("bytes=0-%d", len(obj.Content)))
		}
	}
	if commit {
		s.uploads.Delete(uploadID)
		obj, err = s.createObject(obj)
		if err != nil {
			return jsonResponse{errorMessage: err.Error()}
		}
	} else {
		if _, no308 := r.Header["X-Guploader-No-308"]; no308 {
			// Go client
			responseHeader.Set("X-Http-Status-Code-Override", "308")
		} else {
			// Python client
			status = http.StatusPermanentRedirect
		}
		s.uploads.Store(uploadID, obj)
	}
	if r.Header.Get("X-Goog-Upload-Command") == "upload, finalize" {
		responseHeader.Set("X-Goog-Upload-Status", "final")
	}
	return jsonResponse{
		status: status,
		data:   obj,
		header: responseHeader,
	}
}

// Parse a Content-Range header
// Some possible valid header values:
//   bytes 0-1023/4096 (first 1024 bytes of a 4096-byte document)
//   bytes 1024-2047/* (second 1024 bytes of a streaming document)
//   bytes */4096      (The end of 4096 byte streaming document)
func parseContentRange(r string) (parsed contentRange, err error) {
	invalidErr := fmt.Errorf("invalid Content-Range: %v", r)

	// Require that units == "bytes"
	const bytesPrefix = "bytes "
	if !strings.HasPrefix(r, bytesPrefix) {
		return parsed, invalidErr
	}

	// Split range from total length
	parts := strings.SplitN(r[len(bytesPrefix):], "/", 2)
	if len(parts) != 2 {
		return parsed, invalidErr
	}

	// Process range
	if parts[0] == "*" {
		parsed.Start = -1
		parsed.End = -1
	} else {
		rangeParts := strings.SplitN(parts[0], "-", 2)
		if len(rangeParts) != 2 {
			return parsed, invalidErr
		}
		parsed.KnownRange = true
		parsed.Start, err = strconv.Atoi(rangeParts[0])
		if err != nil {
			return parsed, invalidErr
		}
		parsed.End, err = strconv.Atoi(rangeParts[1])
		if err != nil {
			return parsed, invalidErr
		}
	}

	// Process total length
	if parts[1] == "*" {
		parsed.Total = -1
		if !parsed.KnownRange {
			// Must know either range or total
			return parsed, invalidErr
		}
	} else {
		parsed.KnownTotal = true
		parsed.Total, err = strconv.Atoi(parts[1])
		if err != nil {
			return parsed, invalidErr
		}
	}

	return parsed, nil
}

func loadMetadata(rc io.ReadCloser) (*multipartMetadata, error) {
	defer rc.Close()
	var m multipartMetadata
	err := json.NewDecoder(rc).Decode(&m)
	return &m, err
}

func loadContent(rc io.ReadCloser) ([]byte, error) {
	defer rc.Close()
	return ioutil.ReadAll(rc)
}

func generateUploadID() (string, error) {
	var raw [16]byte
	_, err := rand.Read(raw[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", raw[:]), nil
}
