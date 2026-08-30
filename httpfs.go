// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"io/fs"
	"net/http"
)

// statusPages are the pages whose content announces an HTTP error. Serving
// them with 200 would tell a crawler the error page is a real page.
var statusPages = map[string]int{
	"/401.html": http.StatusUnauthorized,
	"/404.html": http.StatusNotFound,
}

// FileServer returns a handler that serves the contents of fsys, exactly like
// http.FileServerFS, except that the pages listed in statusPages carry the
// status code their content announces.
func FileServer(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code, ok := statusPages[r.URL.Path]; ok {
			w = &statusOverride{ResponseWriter: w, code: code}
		}
		files.ServeHTTP(w, r)
	})
}

// statusOverride replaces the 200 that the wrapped handler would write with
// code. Any other status the handler chooses (a redirect, 304 Not Modified,
// 206 Partial Content, an error) passes through untouched.
type statusOverride struct {
	http.ResponseWriter
	code    int
	written bool
}

func (s *statusOverride) WriteHeader(code int) {
	if s.written {
		return
	}
	s.written = true
	if code == http.StatusOK {
		code = s.code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusOverride) Write(b []byte) (int, error) {
	if !s.written {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (s *statusOverride) Unwrap() http.ResponseWriter { return s.ResponseWriter }
