/*
Copyright 2011 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bufio"
	"bytes"
	"camli/auth"
	"camli/blobref"
	"camli/httputil"
	"fmt"
	"http"
	"os"
	"io"
	"io/ioutil"
	"json"
	"log"
	"strings"
	"time"
)

func createGetHandler(fetcher blobref.Fetcher) func(http.ResponseWriter, *http.Request) {
	return func(conn http.ResponseWriter, req *http.Request) {
		handleGet(conn, req, fetcher)
	}
}

const fetchFailureDelayNs = 200e6 // 200 ms
const maxJsonSize = 64 * 1024     // should be enough for everyone

func sendUnauthorized(conn http.ResponseWriter) {
	conn.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(conn, "<h1>Unauthorized</h1>")
}

func handleGet(conn http.ResponseWriter, req *http.Request, fetcher blobref.Fetcher) {
	isOwner := auth.IsAuthorized(req)

	blobRef := BlobFromUrlPath(req.URL.Path)
	if blobRef == nil {
		httputil.BadRequestError(conn, "Malformed GET URL.")
		return
	}

	var viaBlobs []*blobref.BlobRef
	if !isOwner {
		viaPathOkay := false
		startTime := time.Nanoseconds()
		defer func() {
			if !viaPathOkay {
				// Insert a delay, to hide timing attacks probing
				// for the existence of blobs.
				sleep := fetchFailureDelayNs - (time.Nanoseconds() - startTime)
				if sleep > 0 {
					time.Sleep(sleep)
				}
			}
		}()
		viaBlobs = make([]*blobref.BlobRef, 0)
		if via := req.FormValue("via"); via != "" {
			for _, vs := range strings.Split(via, ",", -1) {
				if br := blobref.Parse(vs); br == nil {
					httputil.BadRequestError(conn, "Malformed blobref in via param")
					return
				} else {
					viaBlobs = append(viaBlobs, br)
				}
			}
		}

		fetchChain := make([]*blobref.BlobRef, 0)
		fetchChain = append(fetchChain, viaBlobs...)
		fetchChain = append(fetchChain, blobRef)
		for i, br := range fetchChain {
			switch i {
			case 0:
				file, size, err := fetcher.Fetch(br)
				if err != nil {
					log.Printf("Fetch chain 0 of %s failed: %v", br.String(), err)
					sendUnauthorized(conn)
					return
				}
				defer file.Close()
				if size > maxJsonSize {
					log.Printf("Fetch chain 0 of %s too large", br.String())
					sendUnauthorized(conn)
					return
				}
				jd := json.NewDecoder(file)
				m := make(map[string]interface{})
				if err := jd.Decode(&m); err != nil {
					log.Printf("Fetch chain 0 of %s wasn't JSON: %v", br.String(), err)
					sendUnauthorized(conn)
					return
				}
				if m["camliType"].(string) != "share" {
					log.Printf("Fetch chain 0 of %s wasn't a share", br.String())
					sendUnauthorized(conn)
					return
				}
				if len(fetchChain) > 1 && fetchChain[1].String() != m["target"].(string) {
					log.Printf("Fetch chain 0->1 (%s -> %q) unauthorized, expected hop to %q",
						br.String(), fetchChain[1].String(), m["target"])
					sendUnauthorized(conn)
					return
				}
			case len(fetchChain) - 1:
				// Last one is fine (as long as its path up to here has been proven, and it's
				// not the first thing in the chain)
				continue
			default:
				file, _, err := fetcher.Fetch(br)
				if err != nil {
					log.Printf("Fetch chain %d of %s failed: %v", i, br.String(), err)
					sendUnauthorized(conn)
					return
				}
				defer file.Close()
				lr := io.LimitReader(file, maxJsonSize)
				slurpBytes, err := ioutil.ReadAll(lr)
				if err != nil {
					log.Printf("Fetch chain %d of %s failed in slurp: %v", i, br.String(), err)
					sendUnauthorized(conn)
					return
				}
				saught := fetchChain[i+1].String()
				if bytes.IndexAny(slurpBytes, saught) == -1 {
					log.Printf("Fetch chain %d of %s failed; no reference to %s",
						i, br.String(), saught)
					sendUnauthorized(conn)
					return
				}
			}
		}
		viaPathOkay = true
	}

	file, size, err := fetcher.Fetch(blobRef)
	switch err {
	case nil:
		break
	case os.ENOENT:
		conn.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(conn, "Object not found.")
		return
	default:
		httputil.ServerError(conn, err)
		return
	}

	defer file.Close()

	reqRange := getRequestedRange(req)
	if reqRange.SkipBytes != 0 {
		_, err = file.Seek(reqRange.SkipBytes, 0)
		if err != nil {
			httputil.ServerError(conn, err)
			return
		}
	}

	var input io.Reader = file
	if reqRange.LimitBytes != -1 {
		input = io.LimitReader(file, reqRange.LimitBytes)
	}

	remainBytes := size - reqRange.SkipBytes
	if reqRange.LimitBytes != -1 &&
		reqRange.LimitBytes < remainBytes {
		remainBytes = reqRange.LimitBytes
	}

	// Assume this generic content type by default.  For better
	// demos we'll try to sniff and guess the "right" MIME type in
	// certain cases (no Range requests, etc) but this isn't part
	// of the Camli spec at all.  We just do it to ease demos.
	contentType := "application/octet-stream"
	if reqRange.IsWholeFile() {
		const peekSize = 1024
		bufReader, _ := bufio.NewReaderSize(input, peekSize)
		header, _ := bufReader.Peek(peekSize)
		if len(header) >= 8 {
			switch {
			case isValidUtf8(string(header)):
				contentType = "text/plain; charset=utf-8"
			case bytes.HasPrefix(header, []byte{0xff, 0xd8, 0xff, 0xe2}):
				contentType = "image/jpeg"
			case bytes.HasPrefix(header, []byte{0x89, 0x50, 0x4e, 0x47, 0xd, 0xa, 0x1a, 0xa}):
				contentType = "image/png"
			}
		}
		input = bufReader
	}

	conn.SetHeader("Content-Type", contentType)
	if !reqRange.IsWholeFile() {
		conn.SetHeader("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", reqRange.SkipBytes,
				reqRange.SkipBytes+remainBytes,
				size))
		conn.WriteHeader(http.StatusPartialContent)
	}
	bytesCopied, err := io.Copy(conn, input)

	// If there's an error at this point, it's too late to tell the client,
	// as they've already been receiving bytes.  But they should be smart enough
	// to verify the digest doesn't match.  But we close the (chunked) response anyway,
	// to further signal errors.
	killConnection := func() {
		closer, _, err := conn.Hijack()
		if err != nil {
			closer.Close()
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error sending file: %v, err=%v\n", blobRef, err)
		killConnection()
		return
	}
	if bytesCopied != remainBytes {
		fmt.Fprintf(os.Stderr, "Error sending file: %v, copied=%d, not %d\n", blobRef,
			bytesCopied, remainBytes)
		killConnection()
		return
	}
}

// TODO: copied this from lib/go/schema, but this might not be ideal.
// unify and speed up?
func isValidUtf8(s string) bool {
	for _, rune := range []int(s) {
		if rune == 0xfffd {
			return false
		}
	}
	return true

}
