// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// Taken from the Go sources and modified by Matthias S. Benkmann

// HTTP file system request handler

package http2

import (
	"net/http"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)


// ServeContent replies to the request using the content in the
// provided Reader.  The main benefit of ServeContent over io.Copy
// is that it handles Range requests properly, sets the MIME type, and
// handles If-Modified-Since requests.
//
// Response's Content-Type header has to be set before calling this
// function.
//
// If modtime is not the zero time or Unix epoch, ServeContent
// includes it in a Last-Modified header in the response.  If the
// request includes an If-Modified-Since header, ServeContent uses
// modtime to decide whether the content needs to be sent at all.
//
// If content implements io.Seeker, a seek to the end
// will be used to determine the size and then a seek to the start
// will be used before reading the content to send.
// The size argument passed to the function is ignored in this case.
//
// If size < 0 and content does not implement io.Seeker,
// "Transfer-Encoding: chunked" will be used and range requests will
// not be supported.
//
// If size >= 0 and content does not support io.Seeker, range requests
// will still be supported as long as they don't request overlapping
// ranges. In this case dummy reads will be used to
// skip parts that are not transmitted. If an overlapping range is requested
// the range request will be ignored and the whole data will be sent.
//
// If the caller has set w's ETag header, ServeContent uses it to
// handle requests using If-Range and If-None-Match.
//
// Note that *os.File implements the io.ReadSeeker interface.
func ServeContent(w http.ResponseWriter, r *http.Request, modtime time.Time, size int64, content io.Reader) {
	var err error
	
	if checkLastModified(w, r, modtime) {
		return
	}
	rangeReq, done := checkETag(w, r, modtime)
	if done {
		return
	}

	code := http.StatusOK

	ctypes := w.Header()["Content-Type"]
	var ctype string
	if len(ctypes) > 0 {
		ctype = ctypes[0]
	}

	seeker, can_seek := content.(io.Seeker)
	if can_seek {
		// seek to end to determine size
		size, err = seeker.Seek(0, os.SEEK_END)
		if err == nil {
			// seek back to start for serving content
			_, err = seeker.Seek(0, os.SEEK_SET)
		}
		if err != nil {
			http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
		        return
		}
	}

	// handle Content-Range header.
	sendSize := size
	var sendContent io.Reader = content
	if size >= 0 {
		ranges, err := parseRange(rangeReq, size, can_seek, !can_seek)
		if err != nil {
			http.Error(w, "416 Requested Range Not Satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if sumRangesSize(ranges) > size {
			// The total number of bytes in all the ranges
			// is larger than the size of the file by
			// itself, so this is probably an attack, or a
			// dumb client.  Ignore the range request.
			ranges = nil
		}
		switch {
		case len(ranges) == 1:
			// RFC 2616, Section 14.16:
			// "When an HTTP message includes the content of a single
			// range (for example, a response to a request for a
			// single range, or to a request for a set of ranges
			// that overlap without any holes), this content is
			// transmitted with a Content-Range header, and a
			// Content-Length header showing the number of bytes
			// actually transferred.
			// ...
			// A response to a request for a single range MUST NOT
			// be sent using the multipart/byteranges media type."
			ra := ranges[0]
			if can_seek {
			  _, err = seeker.Seek(ra.start, os.SEEK_SET)
			} else {
			  err = skip(content, ra.start)
			}
			if err != nil {
				http.Error(w, "416 Requested Range Not Satisfiable", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			sendSize = ra.length
			code = http.StatusPartialContent
			w.Header().Set("Content-Range", ra.contentRange(size))
		case len(ranges) > 1:
			sendSize = rangesMIMESize(ranges, ctype, size)
			code = http.StatusPartialContent

			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			w.Header().Set("Content-Type", "multipart/byteranges; boundary="+mw.Boundary())
			sendContent = pr
			defer pr.Close() // cause writing goroutine to fail and exit if CopyN doesn't finish.
			go func() {
				var offset int64 = 0
				for _, ra := range ranges {
					part, err := mw.CreatePart(ra.mimeHeader(ctype, size))
					if err != nil {
						pw.CloseWithError(err)
						return
					}
					if can_seek {
						_, err = seeker.Seek(ra.start, os.SEEK_SET)
					} else {
						// parseRange() guarantees that ranges
						// don't overlap and are sorted by ascending start
						err = skip(content, ra.start-offset)
					}
					if err != nil {
						pw.CloseWithError(err)
						return
					}
					if _, err := io.CopyN(part, content, ra.length); err != nil {
						pw.CloseWithError(err)
						return
					}
					offset = ra.start + ra.length
				}
				mw.Close()
				pw.Close()
			}()
		}

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(sendSize, 10))
	}

	w.WriteHeader(code)

	if r.Method != "HEAD" {
		if sendSize >= 0 {
			io.CopyN(w, sendContent, sendSize)
		} else {
			io.Copy(w, sendContent)
		}
	}
}

// Reads and discards howmany bytes from r.
func skip(r io.Reader, howmany int64) error {
  var buf [32768]byte
  var err error
  var n int
  for howmany > 0 {
    if howmany > int64(len(buf)) {
      n, err = r.Read(buf[:])
    } else {
      n, err = r.Read(buf[:howmany])
    }
    if n <= 0 && err != nil { return err }
    howmany -= int64(n)
  }
  return nil
}

var unixEpochTime = time.Unix(0, 0)

// modtime is the modification time of the resource to be served, or IsZero().
// return value is whether this request is now complete.
func checkLastModified(w http.ResponseWriter, r *http.Request, modtime time.Time) bool {
	if modtime.IsZero() || modtime.Equal(unixEpochTime) {
		// If the file doesn't have a modtime (IsZero), or the modtime
		// is obviously garbage (Unix time == 0), then ignore modtimes
		// and don't process the If-Modified-Since header.
		return false
	}

	// The Date-Modified header truncates sub-second precision, so
	// use mtime < t+1s instead of mtime <= t to check for unmodified.
	if t, err := time.Parse(http.TimeFormat, r.Header.Get("If-Modified-Since")); err == nil && modtime.Before(t.Add(1*time.Second)) {
		h := w.Header()
		delete(h, "Content-Type")
		delete(h, "Content-Length")
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	w.Header().Set("Last-Modified", modtime.UTC().Format(http.TimeFormat))
	return false
}

// checkETag implements If-None-Match and If-Range checks.
//
// The ETag or modtime must have been previously set in the
// ResponseWriter's headers.  The modtime is only compared at second
// granularity and may be the zero value to mean unknown.
//
// The return value is the effective request "Range" header to use and
// whether this request is now considered done.
func checkETag(w http.ResponseWriter, r *http.Request, modtime time.Time) (rangeReq string, done bool) {
	etag := w.Header().Get("Etag")
	rangeReq = r.Header.Get("Range")

	// Invalidate the range request if the entity doesn't match the one
	// the client was expecting.
	// "If-Range: version" means "ignore the Range: header unless version matches the
	// current file."
	// We only support ETag versions.
	// The caller must have set the ETag on the response already.
	if ir := r.Header.Get("If-Range"); ir != "" && ir != etag {
		// The If-Range value is typically the ETag value, but it may also be
		// the modtime date. See golang.org/issue/8367.
		timeMatches := false
		if !modtime.IsZero() {
			if t, err := http.ParseTime(ir); err == nil && t.Unix() == modtime.Unix() {
				timeMatches = true
			}
		}
		if !timeMatches {
			rangeReq = ""
		}
	}

	if inm := r.Header.Get("If-None-Match"); inm != "" {
		// Must know ETag.
		if etag == "" {
			return rangeReq, false
		}

		// TODO(bradfitz): non-GET/HEAD requests require more work:
		// sending a different status code on matches, and
		// also can't use weak cache validators (those with a "W/
		// prefix).  But most users of ServeContent will be using
		// it on GET or HEAD, so only support those for now.
		if r.Method != "GET" && r.Method != "HEAD" {
			return rangeReq, false
		}

		// TODO(bradfitz): deal with comma-separated or multiple-valued
		// list of If-None-match values.  For now just handle the common
		// case of a single item.
		if inm == etag || inm == "*" {
			h := w.Header()
			delete(h, "Content-Type")
			delete(h, "Content-Length")
			w.WriteHeader(http.StatusNotModified)
			return "", true
		}
	}
	return rangeReq, false
}


// httpRange specifies the byte range to be sent to the client.
type httpRange struct {
	start, length int64
}

func (r httpRange) contentRange(size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.start, r.start+r.length-1, size)
}

func (r httpRange) mimeHeader(contentType string, size int64) textproto.MIMEHeader {
	return textproto.MIMEHeader{
		"Content-Range": {r.contentRange(size)},
		"Content-Type":  {contentType},
	}
}

// parseRange parses a Range header string as per RFC 2616.
// If overlap_allowed == false, an error will be returned if
// multiple ranges are requested that overlap.
// If sorted==true, the ranges will be returned in ascending order
// of start offset. If sorted==false, ranges will be returned in the
// order in which they occur in s.
// overlap_allowed == false implies sorted == true
func parseRange(s string, size int64, overlap_allowed bool, sorted bool) ([]httpRange, error) {
	if s == "" {
		return nil, nil // header not present
	}
	const b = "bytes="
	if !strings.HasPrefix(s, b) {
		return nil, errors.New("invalid range")
	}
	var ranges []httpRange
	for _, ra := range strings.Split(s[len(b):], ",") {
		ra = strings.TrimSpace(ra)
		if ra == "" {
			continue
		}
		i := strings.Index(ra, "-")
		if i < 0 {
			return nil, errors.New("invalid range")
		}
		start, end := strings.TrimSpace(ra[:i]), strings.TrimSpace(ra[i+1:])
		var r httpRange
		if start == "" {
			// If no start is specified, end specifies the
			// range start relative to the end of the file.
			i, err := strconv.ParseInt(end, 10, 64)
			if err != nil {
				return nil, errors.New("invalid range")
			}
			if i > size {
				i = size
			}
			r.start = size - i
			r.length = size - r.start
		} else {
			i, err := strconv.ParseInt(start, 10, 64)
			if err != nil || i >= size || i < 0 {
				return nil, errors.New("invalid range")
			}
			r.start = i
			if end == "" {
				// If no end is specified, range extends to end of the file.
				r.length = size - r.start
			} else {
				i, err := strconv.ParseInt(end, 10, 64)
				if err != nil || r.start > i {
					return nil, errors.New("invalid range")
				}
				if i >= size {
					i = size - 1
				}
				r.length = i - r.start + 1
			}
		}
		ranges = append(ranges, r)
	}
	
	// sort ranges by ascending start
	// insertion sort
	if sorted || !overlap_allowed {
		for x := 1; x < len(ranges); x++ {
			child_to_find_place_for := ranges[x]
			y := x
			for y > 0 && ranges[y-1].start > child_to_find_place_for.start {
				ranges[y] = ranges[y-1]
				y--
			}
			ranges[y] = child_to_find_place_for
		}
	}
	
	if !overlap_allowed {
		for x := 1; x < len(ranges); x++ {
			if ranges[x].start < ranges[x-1].start + ranges[x-1].length {
				return nil, errors.New("overlapping ranges")
			}
		}
	}
	
	return ranges, nil
}

// countingWriter counts how many bytes have been written to it.
type countingWriter int64

func (w *countingWriter) Write(p []byte) (n int, err error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

// rangesMIMESize returns the number of bytes it takes to encode the
// provided ranges as a multipart response.
func rangesMIMESize(ranges []httpRange, contentType string, contentSize int64) (encSize int64) {
	var w countingWriter
	mw := multipart.NewWriter(&w)
	for _, ra := range ranges {
		mw.CreatePart(ra.mimeHeader(contentType, contentSize))
		encSize += ra.length
	}
	mw.Close()
	encSize += int64(w)
	return
}

func sumRangesSize(ranges []httpRange) (size int64) {
	for _, ra := range ranges {
		size += ra.length
	}
	return
}
