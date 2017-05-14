// Copyright 2017 Francisco Souza. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fakestorage

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/gorilla/mux"
)

// Object represents the object that is stored within the fake server.
type Object struct {
	BucketName string
	Name       string
	Content    []byte
}

func (o *Object) id() string {
	return o.BucketName + "/" + o.Name
}

// CreateObject stores the given object internally.
//
// If the bucket within the object doesn't exist, it also creates it. If the
// object already exists, it overrides the object.
func (s *Server) CreateObject(obj Object) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	index := s.findObject(obj)
	if index < 0 {
		s.buckets[obj.BucketName] = append(s.buckets[obj.BucketName], obj)
	} else {
		s.buckets[obj.BucketName][index] = obj
	}
}

// ListObjects returns a sorted list of objects that match the given criteria,
// or an error if the bucket doesn't exist.
func (s *Server) ListObjects(bucketName, prefix, delimiter string) ([]Object, error) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	objects, ok := s.buckets[bucketName]
	if !ok {
		return nil, errors.New("bucket not found")
	}
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Name < objects[j].Name
	})
	var respObjects []Object
	for _, obj := range objects {
		if strings.HasPrefix(obj.Name, prefix) {
			objName := strings.Replace(obj.Name, prefix, "", 1)
			if delimiter == "" || !strings.Contains(objName, delimiter) {
				respObjects = append(respObjects, obj)
			}
		}
	}
	return respObjects, nil
}

// GetObject returns the object with the given name in the given bucket, or an
// error if the object doesn't exist.
func (s *Server) GetObject(bucketName, objectName string) (Object, error) {
	obj := Object{BucketName: bucketName, Name: objectName}
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	index := s.findObject(obj)
	if index < 0 {
		return obj, errors.New("object not found")
	}
	return s.buckets[bucketName][index], nil
}

// findObject looks for an object in its bucket and return the index where it
// was found, or -1 if the object doesn't exist.
//
// It doesn't lock the mutex, callers must lock the mutex before calling this
// method.
func (s *Server) findObject(obj Object) int {
	for i, o := range s.buckets[obj.BucketName] {
		if obj.id() == o.id() {
			return i
		}
	}
	return -1
}

func (s *Server) listObjects(w http.ResponseWriter, r *http.Request) {
	bucketName := mux.Vars(r)["bucketName"]
	prefix := r.URL.Query().Get("prefix")
	delimiter := r.URL.Query().Get("delimiter")
	objs, err := s.ListObjects(bucketName, prefix, delimiter)
	encoder := json.NewEncoder(w)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		errResp := newErrorResponse(http.StatusNotFound, "Not Found", nil)
		encoder.Encode(errResp)
		return
	}
	encoder.Encode(newListObjectsResponse(objs, s))
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	encoder := json.NewEncoder(w)
	obj, err := s.GetObject(vars["bucketName"], vars["objectName"])
	if err != nil {
		errResp := newErrorResponse(http.StatusNotFound, "Not Found", nil)
		w.WriteHeader(http.StatusNotFound)
		encoder.Encode(errResp)
		return
	}
	encoder.Encode(newObjectResponse(obj, s))
}

func (s *Server) downloadObject(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	obj, err := s.GetObject(vars["bucketName"], vars["objectName"])
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write(obj.Content)
}
