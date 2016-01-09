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


package main

import (
         "os"
         "fmt"
         "net"
         "net/http"
         "path"
         "sync"
         "time"
         "strings"
         "strconv"
         "syscall"
         "github.com/mbenkmann/golib/argv"
         "github.com/mbenkmann/golib/util"
         
         "../linux"
)

const QUICKSTART = `Quickstart instructions:

1. Create/Choose a <directory> whose files Garçon should serve.
   You can start with an empty directory and use the web interface
   to upload files.
   This is particularly useful to start a fresh Debian repository.

2. Change its UID/GID ownership to what the Garçon process should use
   (If started as root, Garçon will change to this UID/GID).

3. Run 
      garçon --dir <directory> &

To read the full manual, run
      garçon --help
`

const (
  UNKNOWN = iota
  HELP
  ROOT
  UID
  GID
  CHROOT
  HTTP
  VERBOSE
)

const DISABLED = 0
const ENABLED = 1

var usage = argv.Usage{
{ UNKNOWN, 1, "", "",        argv.ArgUnknown, `NAME
    garçon - web server with special features for hosting Debian repositories

SYNOPSIS
    garçon [OPTIONS] --directory=serverroot

OPTIONS
    Long options can be written as "-directory foo", "-directory=foo",
    "--directory foo" or "--directory=foo". They can be abbreviated,
    e.g. --dir for --directory. Short options can be grouped.
`},
{ 0,0,"","",argv.ArgUnknown,"\f" },
{ HELP,1,  "","help",     argv.ArgNone,       "    --help \tPrint usage and exit.\n" },
{ ROOT,1, "d","directory",argv.ArgRequired,   "    -d dir, --directory=dir \tRoot of the directory tree to serve. Garçon will chroot into this directory by default.\n" },
{ HTTP,1, "","http-port" ,argv.ArgInt,        "    --http-port=number \tPort to listen on for HTTP connections. Default is 80.\n" },
{ UID,1,  "u","uid",      argv.ArgRequired,   "    -u uid, --uid=uid \tUID the Garçon process should run as. Defaults to the owner of the server root set with --directory.\n" },
{ GID,1,  "g","gid",      argv.ArgRequired,   "    -g gid, --gid=gid \tGID the Garçon process should run as. Defaults to the group of the server root set with --directory.\n" },
{ CHROOT,ENABLED,  "" ,"enable-chroot", argv.ArgNone,   "    --enable-chroot \tMakes Garçon chroot into the server root set with --directory. This is the default, but this switch can be used to undo the effect of a --disable-chroot earlier on the command line.\n" },
{ CHROOT,DISABLED,  "","disable-chroot",argv.ArgNone,   "    --disable-chroot \tDisables the default behaviour of chrooting into the server root set with --directory. This will allow symlinks to point outside of the server root. This is a security risk.\n" },
{ VERBOSE,1,"v","verbose",argv.ArgNone,       "    -v, --verbose \tIncrease verbosity of log output. More -v switches mean more verbosity.\n" },
{ UNKNOWN, 1, "", "",     argv.ArgUnknown, `COPYRIGHT
    Copyright (c) 2016 Matthias S. Benkmann
    Licensed under GPLv3
` },
}

func check(what string, err error) {
  if err != nil {
    util.Log(0, "ERROR! %v: %v\n", what, err)
    util.LoggersFlush(5*time.Second)
    os.Exit(1)
  }
}


func main() {
  util.LogLevel = 1
  
  argv.LastColumnMinPercent = 100 // Force last column on its own line
  argv.LastColumnOwnLineMaxPercent = 90 // Set indendation to 8 characters for 80 columns screen
  width, err := strconv.Atoi(os.Getenv("COLUMNS"))
  if err != nil { width = 80 }
  if width > 100 { width = 100 } // more than 100 columns makes usage hard to read
  argv.Columns = width

  if len(os.Args) == 1 {
    fmt.Fprintf(os.Stdout, "%v\n", QUICKSTART)
    os.Exit(0)
  }

  options, _, err, _ := argv.Parse(os.Args[1:], usage, "gnu -perl --abb")
  check("parse command line",err)

  util.LogLevel = options[VERBOSE].Count()
  
  if options[HELP].Is(ENABLED) {
    fmt.Fprintf(os.Stdout, "%v\n", usage)
    os.Exit(0)
  }

  if options[ROOT].Count() == 0 {
    fmt.Fprintf(os.Stderr, "You need to specify the server root --directory\n")
    os.Exit(1)
  }
  
  err = os.Chdir(options[ROOT].Last().Arg)
  check("chdir",err)
  
  wd, err := os.Getwd()
  if err != nil { wd = options[ROOT].Last().Arg }
  
  uid := 0
  gid := 0
  if options[UID].Count() == 0 || options[GID].Count() == 0 {
    fi, err := os.Stat(".")
    check("stat",err)
    uid = int(fi.Sys().(*syscall.Stat_t).Uid)
    gid = int(fi.Sys().(*syscall.Stat_t).Gid)
  }
  
  if options[UID].Count() > 0 {
    uid, err = linux.Getuid(options[UID].Last().Arg)
    check("getuid",err)
  }
  
  if options[GID].Count() > 0 {
    gid, err = linux.Getgid(options[GID].Last().Arg)
    check("getgid",err)
  }
  
  http_port := "80"
  if options[HTTP].Count() > 0 {
    http_port = options[HTTP].Last().Arg
    if options[HTTP].Last().Value.(int) <= 0 || options[HTTP].Last().Value.(int) > 65535 {
      check("--http-port",fmt.Errorf("Illegal HTTP port: %v", http_port))
    }
  }
  
  util.Log(1, "Server root: %v", wd)
  util.Log(1, "Process UID: %v", uid)
  util.Log(1, "Process GID: %v", gid)
  util.Log(1, "HTTP   port: %v", http_port)
  
  // Create listeners before dropping privileges
  var https_listener net.Listener
  http_listener, err := net.Listen("tcp", ":"+http_port)
  check("listen",err)
  
  if !options[CHROOT].Is(DISABLED) {
    util.Log(1, "Chrooting into %v", wd)
    err = syscall.Chroot(".")
    check("chroot",err)
  }
  
  // Setgid() before Setuid() because after Setuid() we no longer have permission to do Setgid()
  if syscall.Getgid() != gid {
    util.Log(1, "setgid(%v)", gid)
    err = linux.Setgid(gid)
    check("setgid",err)
  }
  
  if syscall.Getuid() != uid {
    util.Log(1, "setuid(%v)", uid)
    err = linux.Setuid(uid)
    check("setuid",err)
  }


  server := &http.Server{
              Handler: nil, // => DefaultServeMux
            }

  wd, err = os.Getwd() // if we have chrooted, wd is now "/"
  fm,err := NewFileManager(wd)
  check("scan files",err)
  
  go fm.AutoUpdate()
  
  http.Handle("/", fm)
	
  if https_listener != nil {
    go func() {
      e := server.Serve(https_listener)
      check("serve https",e)
    }() 
  }
  
  e := server.Serve(http_listener)
  check("serve http",e)
}

/*
  Each directory entry has a unique number that is changed whenever
  mtime changes. This number is used both as cache key and as ETag.
  The <<10 for the init value of the counter makes sure that
  numbers do not repeat even if the server is restarted. Even if a
  repeat happened it would only be a problem if a number were repeated
  for a file that has changed and that has used the repeated number
  earlier and some browser still has it stored as ETag.
*/
var nextid = util.Counter(uint64(time.Now().Unix()) << 10)

// Each file can have multiple cache entries (plain, gzipped,...)
const NUM_CACHES_PER_FILE = 1

type FileManager struct {
  cache map[uint64][NUM_CACHES_PER_FILE]interface{}
  tree map[string]*File
  inotify int
  rootdir string
  mutex sync.RWMutex
}

type File struct {
  Info os.FileInfo
  Id uint64
  Contents map[string]*File //only if IsDir()
}

func (f *File) ModTime() time.Time {
  return f.Info.ModTime()
}

func (f *File) IsDir() bool {
  return f.Info.IsDir()
}

func NewFileManager(rootdir string) (*FileManager, error) {
  fm := &FileManager{tree:map[string]*File{}, inotify:-1, rootdir:rootdir}
  err := fm.scan(rootdir, map[string]*File{}, fm.tree)
  if err != nil { return nil, err }
  return fm, nil
}

func (fm *FileManager) hidden(name string) bool {
  return name == "" || name[0] == '.' || name[len(name)-1] == '~'
}

var empty = map[string]*File{}

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
    err = fm.scan(fm.rootdir, fm.tree, newtree)
    if err != nil { 
      util.Log(0, "ERROR! re-scan: %v", err)
      time.Sleep(30*time.Second)
    } else {
      fm.mutex.Lock()
      fm.tree = newtree
      fm.mutex.Unlock()
      time.Sleep(5*time.Second)
    }
  }
}

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
  for _, fi := range fis {
    name := fi.Name()
    if fm.hidden(name) { 
      util.Log(2, "Hidden: %v", name)
      continue
    }
    n := &File{Info:fi}
  
    cur[name] = n
    if o, ok := old[name]; ok && o.ModTime().Equal(fi.ModTime()) && o.IsDir() == n.IsDir() {
      util.Log(2, "Unchanged: %v", name)
      n.Id = o.Id
    } else {
      util.Log(2, "New/Changed: %v", name)
      n.Id = <-nextid
    }
    
    if n.IsDir() {
      dirs = append(dirs, name)
      n.Contents = map[string]*File{}
    }
  }
  
  util.Log(2, "Subdirectories to scan: %v", dirs)
  for _, subdir := range dirs {
    o := old[subdir]
    oldmap := empty
    if o != nil && o.IsDir() {
      oldmap = o.Contents
    }
    err = fm.scan(path.Join(dir, subdir), oldmap, cur[subdir].Contents)
    if err != nil { return err }
  }
  
  return nil
}

func (fm *FileManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
  switch r.Method {
    case "", "GET", "HEAD": // OK, we support these
    default: w.Header().Set("Allow", "GET, HEAD")
             util.Log(1, "%v %v %v", http.StatusMethodNotAllowed, r.Method, r.URL.Path)
             http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
             return
  }
   
  clean := path.Clean(r.URL.Path)
  if clean != "" && clean[len(clean)-1] == '/' { clean = clean[0:len(clean)-1] }
  if clean == "." || clean == "" { clean = "/" }
  
  if clean != r.URL.Path {
    util.Log(2, "%v => %v", r.URL.Path, clean)
  }
  
  what := strings.Split(clean,"/")
  
  where := "."
  
  var x *File
  ok := false
  fm.mutex.RLock()
  {
    dir := fm.tree
    for _, name := range what {
      if name == "" { continue }
      where = where + "/" + name
      if x, ok = dir[name]; !ok {
        break
      }
      if x.IsDir() {
        dir = x.Contents
      } else {
        dir = empty
      }
    }
  }
  fm.mutex.RUnlock()
  
  if !ok || x.IsDir() {
    util.Log(1, "%v %v %v", http.StatusNotFound, r.Method, r.URL.Path)
    http.NotFound(w,r)
    return
  }
  
  f, err := os.Open(where)
  if err != nil {
    util.Log(0, "ERROR! open(%v): %v", where, err)
    util.Log(0, "%v %v %v", http.StatusInternalServerError, r.Method, r.URL.Path)
    http.Error(w, "internal server error", http.StatusInternalServerError)
    return
  }
  defer f.Close()
  
  w.Header().Set("ETag", fmt.Sprintf("%v", x.Id))
  //w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%v",max_age))
  mime := linux.Extension2MIME[path.Ext(where)]
  if mime == "" { mime = "application/octet-stream" }
  if strings.HasPrefix(mime, "text/") {
    mime += "; charset=UTF-8"
  }
  w.Header().Set("Content-Type", mime)
  
  util.Log(0, "%v %v %v (ETag: %v, Content-Type: %v)", http.StatusOK, r.Method, r.URL.Path, x.Id, mime)
  http.ServeContent(w,r,"",x.ModTime(),f)
}


