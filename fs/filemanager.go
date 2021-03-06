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
         "fmt"
         "net/http"
         "path"
         "sync"
         "time"
         "strings"
         "syscall"
         "github.com/mbenkmann/golib/util"
         
         "../linux"
         "../http2"
)

/*
  Creates and returns a new FileManager. Does not return until the directory tree has been
  scanned. From then on the directory tree will remain fixed unless you call AutoUpdate().
  
    rootdir: The path of the root of the directory tree
    handling: Special rules for handling certain files
*/
func NewFileManager(rootdir string, handling []Handling) (*FileManager, error) {
  root := &File{
    Info: &FileInfo{"",0,os.ModeDir|0777,time.Now(),true},
    Id:0,
    Contents:map[string]*File{},
    Gzip:false,
    Data:rootdir,
  }
  fm := &FileManager{root:root, inotify:-1, handling:handling}
  err := fm.scan(rootdir, map[string]*File{}, root.Contents)
  if err != nil { return nil, err }
  AddIndexes(root.Contents, "Home")
  return fm, nil
}

/*
  Answers the HTTP request r by writing to w the appropriate file
  managed by this FileManager.
*/
func (fm *FileManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
  var err error
  
  switch r.Method {
    case "", "GET", "HEAD": // OK, we support these
    default: w.Header().Set("Allow", "GET, HEAD")
             util.Log(1, "%v %v %v", http.StatusMethodNotAllowed, r.Method, r.URL.Path)
             http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
             return
  }

  clean := path.Clean(r.URL.Path)
  // remove trailing slash
  if clean != "" && clean[len(clean)-1] == '/' { clean = clean[0:len(clean)-1] }
  // turn "", "." and "/" into "/index.html"
  if clean == "." || clean == "" || clean == "/" { clean = "/index.html" }
  
  if clean != r.URL.Path {
    util.Log(2, "Rewrite %v => %v", r.URL.Path, clean)
  }
  
  what := strings.Split(clean,"/")
  
  var x *File
  ok := false
  fm.mutex.RLock()
  {
    dir := fm.root.Contents
    for _, name := range what {
      if name == "" { continue }
      if x, ok = dir[name]; !ok {
        break
      }
      if x.Info.IsDir() {
        dir = x.Contents
      } else {
        dir = empty
      }
    }
    
    if ok && x.Info.IsDir() {
      util.Log(2, "Rewrite %v => %v", r.URL.Path, clean + "/index.html")
      x, ok = dir["index.html"]
    }
  }
  fm.mutex.RUnlock()
  
  if !ok || x.Info.IsDir() {
    util.Log(1, "%v %v %v", http.StatusNotFound, r.Method, r.URL.Path)
    http.NotFound(w,r)
    return
  }
  
  understands_gzip := false
  for _, aes := range r.Header["Accept-Encoding"] {
    for _, ae := range strings.Split(aes, ",") {
      ae = strings.TrimSpace(ae)
      understands_gzip = understands_gzip || (ae == "gzip")
    }
  }

  var serve_content io.Reader
  
  gzipped := false
  
  if serve_content == nil {
    var f io.ReadCloser
    f, gzipped, err = x.GetStream(understands_gzip)
    if err != nil {
      util.Log(0, "ERROR! GetStream(): %v", err)
      util.Log(0, "%v %v %v", http.StatusInternalServerError, r.Method, r.URL.Path)
      http.Error(w, "internal server error", http.StatusInternalServerError)
      return
    }
    defer f.Close()
    
    serve_content = f
  }
    
  ce := ""
  if gzipped {
    w.Header().Set("Content-Encoding", "gzip")
    ce=", Content-Encoding: gzip"
  }
  
  w.Header().Set("ETag", fmt.Sprintf("%v", x.Id))
  //w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%v",max_age))
  mime := linux.Extension2MIME[path.Ext(clean)]
  if mime == "" { 
    // Special case for common tarball extensions
    if strings.HasSuffix(clean, ".tar.gz") || strings.HasSuffix(clean, ".tar.xz") || strings.HasSuffix(clean, ".tar.bz2") {
      mime = linux.Extension2MIME[".tgz"]
    } else {
      mime = "application/octet-stream"
    }
  }
  if strings.HasPrefix(mime, "text/") {
    mime += "; charset=UTF-8"
  }
  w.Header().Set("Content-Type", mime)
  
  util.Log(0, "%v %v %v (ETag: %v, Content-Type: %v%v)", http.StatusOK, r.Method, r.URL.Path, x.Id, mime, ce)
  http2.ServeContent(w,r,x.Info.ModTime(),-1,serve_content)
}

/*
  Continuously watches the directory tree of fm and updates the internal
  data if necessary. Never returns. Call in a goroutine.
*/
func (fm *FileManager) AutoUpdate() {
  var buf [1024]byte
  var err error
  
  for {
    if fm.inotify >= 0 {
      _, err = syscall.Read(fm.inotify, buf[:])
      if err != nil {
        util.Log(0, "ERROR! inotify read: %v", err)
      }
      err = syscall.Close(fm.inotify)
      fm.inotify = -1
      if err != nil {
        util.Log(0, "ERROR! inotify close: %v", err)
      }
    }
    newtree := map[string]*File{}
    err = fm.scan(fm.root.Data.(string), fm.root.Contents, newtree)
    if err != nil { 
      util.Log(0, "ERROR! re-scan: %v", err)
      time.Sleep(30*time.Second)
    } else {
      AddIndexes(newtree, "Home")
      fm.mutex.Lock()
      fm.root.Contents = newtree
      fm.mutex.Unlock()
      time.Sleep(5*time.Second)
    }
  }
}


// Handles a directory tree.
type FileManager struct {
  // inotify file descriptor used to watch all directories for changes.
  inotify int
  
  // The root directory.
  root *File
  
  // Whenever tree is accessed, this mutex is used to protect
  // ServerHTTP() from AutoUpdate()
  mutex sync.RWMutex
  
  // The handling rules for file patterns.
  handling []Handling
}

/*
  Scan directory dir and add entries to cur. If an entry with the same
  name exists in old, its Id will be reused if the file has not changed.
*/
func (fm *FileManager) scan(dir string, old, cur map[string]*File) error {
  var err error
  // We need to set up inotify before Readdir(), or we might miss some
  // entries added just between Readdir() and inotify.
  if fm.inotify < 0 {
    fm.inotify, err = syscall.InotifyInit()
    if err != nil { return err }
  }
  
  _, err = syscall.InotifyAddWatch(fm.inotify, dir, syscall.IN_CLOSE_WRITE|syscall.IN_CREATE|syscall.IN_DELETE|syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF|syscall.IN_MOVED_FROM|syscall.IN_MOVED_TO|syscall.IN_ONESHOT)
  if err != nil { return err }
  
  util.Log(2, "Scanning: %v", dir)
  d, err := os.Open(dir)
  if err != nil { return err }
  fis, err := d.Readdir(-1)
  d.Close()
  if err != nil { return err }
  
  dirs := []string{}
  aliases1 := []string{}
  aliases2 := []*File{}
  
  for _, fi := range fis {
    name := fi.Name()
    
    hand := 0
    for hand < len(fm.handling) {
      if fm.handling[hand].Match.MatchString(name) { break }
      hand++
    }
    // NOTE: Because fm.handling has a catch-all, it is guaranteed that
    // fm.handling[hand] is valid
    
    n := &File{Info:fi, Data:dir}
    
    unchanged := false
    if o, ok := old[name]; ok && o.Info.ModTime().Equal(fi.ModTime()) && o.Info.IsDir() == n.Info.IsDir() {
      n.Id = o.Id
      unchanged = true
    } else {
      n.Id = <-nextid
    }
    
    // We check for and store aliases before checking for hidden,
    // because in the future we may use the alias mechanism combined with
    // hide to get the alias and hide the original from the index
    if !n.Info.IsDir() && fm.handling[hand].Gzip != "" {
      alias := fm.handling[hand].Match.ReplaceAllString(name, fm.handling[hand].Gzip)
      aliases1 = append(aliases1, alias)
      ali_n := *n
      ali_n.Gzip = true
      aliases2 = append(aliases2, &ali_n)
    }
    
    if fm.handling[hand].Hide { 
      util.Log(2, "Hidden: %v", name)
      continue
    }
    
    if unchanged {
      util.Log(2, "Unchanged: %v", name)
    } else {
      util.Log(2, "New/Changed: %v", name)
    }
    
    cur[name] = n
    
    if n.Info.IsDir() {
      dirs = append(dirs, name)
      n.Contents = map[string]*File{}
    }
  }
  
  for i := range aliases1 {
    if _, conflict := cur[aliases1[i]]; conflict {
      util.Log(2, "Gzip alias %v => %v conflicts with real file or other alias => SKIPPED", aliases1[i], aliases2[i].Info.Name())
    } else {
      util.Log(2, "Gzip alias %v => %v", aliases1[i], aliases2[i].Info.Name())
      cur[aliases1[i]] = aliases2[i]
    }
  }
  
  util.Log(2, "Subdirectories to scan: %v", dirs)
  for _, subdir := range dirs {
    o := old[subdir]
    oldmap := empty
    if o != nil && o.Info.IsDir() {
      oldmap = o.Contents
    }
    err = fm.scan(path.Join(dir, subdir), oldmap, cur[subdir].Contents)
    if err != nil { return err }
  }
  
  return nil
}
