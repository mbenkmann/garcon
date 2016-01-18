/*
Copyright (c) 2016 Matthias S. Benkmann

This program is free software; you can redistribute it and/or
modify it under the terms of the GNU General Public License
as published by the Free Software Foundation; version 3
of the License (ONLY this version).

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.
*/


package fs

import (
         "io"
         "os"
         "time"
         "bytes"
         "regexp"
         "compress/gzip"
         "github.com/mbenkmann/golib/util"
)




// Defines a special handling rule for files matching a pattern.
type Handling struct {
  // The pattern a file name has to match for this handling rule to apply.
  Match *regexp.Regexp
  
  // If hide==true, this file will neither be served nor appear in the index.
  Hide bool
  
  // If not "", this is a replacement pattern that may include backreferences to
  // the match. After the replacement is applied, the replaced name will be
  // registered as an alias for the file that will be delivered with
  // Content-Encoding: gzip. Has no effect on directories.
  Gzip string
}

/*
  A simple implementation of os.FileInfo to use for in-memory files.
*/
type FileInfo struct {
  name string       // base name of the file
  size int64        // length in bytes for regular files; system-dependent for others
  mode os.FileMode     // file mode bits
  modTime time.Time // modification time
  isDir bool        // abbreviation for Mode().IsDir()
}

func (f *FileInfo) Name() string {
  return f.name
}
func (f *FileInfo) Size() int64 {
  return f.size
}
func (f *FileInfo) Mode() os.FileMode {
  return f.mode
}
func (f *FileInfo) ModTime() time.Time {
  return f.modTime
}
func (f *FileInfo) IsDir() bool {
  return f.isDir
}
func (*FileInfo) Sys() interface{} {
  return nil
}



// A directory entry managed by FileManager.
type File struct {
  // Stat of the file.
  Info os.FileInfo
  
  // Each directory entry has a unique number that is changed whenever
  // mtime changes. This number is used as ETag.
  Id uint64
  
  // If Info.IsDir() this is a map of the contents of the directory.
  // May include aliases generated through Handling.gzip.
  Contents map[string]*File
  
  // true iff this is an alias for a gzipped file that is to be served
  // with Content-Encoding: gzip.
  Gzip bool
  
  // The meaning depends on the data type:
  //   string: The path of the filesystem directory containing the file.
  //           By appending "/" + Info.Name(), you get the path for os.Open().
  //   []byte: The raw data of this file.
  Data interface{}
}

func (f *File) String() string {
  switch data := f.Data.(type) {
    case string:
      return data+"/"+f.Info.Name()
    case []byte:
      return "(in-memory)"+f.Info.Name()
    default: return "???"
  }
}

/*
  Returns the File's data.
  
  keep_gzipped: if true and the file is gzipped, return it as is.
                if false and the file is gzipped, return the decompressed data.
                if the file is not gzipped, no effect.
  
  Returns:
    stream: the data, this may or may not implement io.Seeker
    is_gzipped: true if stream is gzipped. if keep_gzipped is false, this is always false.
    err: if an error has occurred
  
  NOTE: If err!=nil, the caller must call stream.Close() when done.
*/
func (f *File) GetStream(keep_gzipped bool) (stream io.ReadCloser, is_gzipped bool, err error) {
  switch data := f.Data.(type) {
    case string:
      stream, err = os.Open(data+"/"+f.Info.Name())
      if err != nil { return }
      
    case []byte:
      stream = &BytesReadCloser{*bytes.NewReader(data)}
    
    default: panic("Unexpected Data type")
  }

  is_gzipped = f.Gzip
  if keep_gzipped || !is_gzipped { return }
  // If we get here, keep_gzipped == false, but is_gzipped == true, so we need a wrapper
  is_gzipped = false
  stream, err = NewGunzipper(stream)
  return
}

type BytesReadCloser struct {
  bytes.Reader
}

func (*BytesReadCloser) Close() error {return nil}

/*
  Takes a gzipped stream and returns a ReadCloser from which you can
  read the ungzipped data. Unlike the stream returned by gzip.NewReader()
  this one closes the original stream when Close() is called on the
  unzipper (provided the original stream implements io.Closer).
*/
func NewGunzipper(gzipped io.Reader) (io.ReadCloser, error) {
  g, err := gzip.NewReader(gzipped)
  if err != nil { return nil, err }
  return &Gunzipper{g,gzipped}, nil
}

type Gunzipper struct {
  gunzip io.ReadCloser
  orig io.Reader
}

func (gunz *Gunzipper) Read(p []byte) (n int, err error) {
  return gunz.gunzip.Read(p)
}

func (gunz *Gunzipper) Close() error {
  err1 := gunz.gunzip.Close()
  if closer, can_be_closed := gunz.orig.(io.Closer); can_be_closed {
    err2 := closer.Close()
    if err2 != nil { return err2 }
  }
  return err1
}




/*
  The <<10 for the init value of the counter makes sure that
  numbers do not repeat even if the server is restarted. Even if a
  repeat happened it would only be a problem if a number were repeated
  for a file that has changed and that has used the repeated number
  earlier and some browser still has it stored as ETag.
*/
var nextid = util.Counter(uint64(time.Now().Unix()) << 10)


var empty = map[string]*File{}
