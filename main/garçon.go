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
         "io"
         "io/ioutil"
         "os"
         "fmt"
         "net"
         "net/http"
         "path"
         "sync"
         "time"
         "bytes"
         "regexp"
         "strings"
         "strconv"
         "syscall"
         "compress/gzip"
         "github.com/mbenkmann/golib/argv"
         "github.com/mbenkmann/golib/util"
         
         "../linux"
         "../http2"
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
  CACHE
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
{ CACHE,1, "","cache-size",argv.ArgInt,       "    --cache-size=number \tStore up to <number> megabytes of most-recently requested files in memory. See also GZIP section below.\n" },
{ HTTP,1, "","http-port" ,argv.ArgInt,        "    --http-port=number \tPort to listen on for HTTP connections. Default is 80.\n" },
{ UID,1,  "u","uid",      argv.ArgRequired,   "    -u uid, --uid=uid \tUID the Garçon process should run as. Defaults to the owner of the server root set with --directory.\n" },
{ GID,1,  "g","gid",      argv.ArgRequired,   "    -g gid, --gid=gid \tGID the Garçon process should run as. Defaults to the group of the server root set with --directory.\n" },
{ CHROOT,ENABLED,  "" ,"enable-chroot", argv.ArgNone,   "    --enable-chroot \tMakes Garçon chroot into the server root set with --directory. This is the default, but this switch can be used to undo the effect of a --disable-chroot earlier on the command line.\n" },
{ CHROOT,DISABLED,  "","disable-chroot",argv.ArgNone,   "    --disable-chroot \tDisables the default behaviour of chrooting into the server root set with --directory. This will allow symlinks to point outside of the server root. This is a security risk.\n" },
{ VERBOSE,1,"v","verbose",argv.ArgNone,       "    -v, --verbose \tIncrease verbosity of log output. More -v switches mean more verbosity.\n" },
{ 0, 0, "", "",argv.ArgUnknown, "\f" },
{ UNKNOWN, 1, "", "",     argv.ArgUnknown, `CONTENT-ENCODING: GZIP

To reduce bandwidth usage and improve page load times, files in text-based formats like HTML and CSS can be compressed with gzip. When Garçon sees a file with an extension from the following table, in addition to serving it under its actual name, Garçon will serve it under an alternative name. If the alternative name is requested by a client without gzip support, Garçon will decompress it unless the file is larger than 10% of the cache size. Otherwise Garçon will send the gzipped data with an added "Content-Encoding: gzip" header. If an actual file with the translated name exists, that will take precedence.

`+"    .svgz \t=> .svg\n"+
  "    .svg.gz \t=> .svg\n"+
  "    .css.gz \t=> .css\n"+
  "    .js.gz \t=> .js\n"+
  "    .json.gz \t=> .json\n"+
  "    .ps.gz \t=> .ps\n"+
  "    .pdf.gz \t=> .pdf\n"+
  "    .txt.gz \t=> .txt\n"+
  "    .xml.gz \t=> .xml\n"+
  "    .xhtml.gz \t=> .xhtml\n"+
  "    .htm.gz \t=> .htm\n"+
  "    .html.gz \t=> .html"+`
    .gz (no other dots in file name) => no extension

The following command will compress all files with supported extensions. Files with no extension need to be compressed separately.

    gzip *.html *.htm *.css *.js *.xml *.xhtml *.txt *.svg *.json *.ps *.pdf
` },

{ UNKNOWN, 1, "", "",     argv.ArgUnknown, `COPYRIGHT
    Copyright (c) 2016 Matthias S. Benkmann
    Licensed under GPLv3
` },
}

/*
  If err is not nil, logs the error prefixed by "what: " and then quits the program with code 1.
*/
func check(what string, err error) {
  if err != nil {
    util.Log(0, "ERROR! %v: %v\n", what, err)
    util.LoggersFlush(5*time.Second)
    os.Exit(1)
  }
}

// Defines a special handling rule for files matching a pattern.
type Handling struct {
  // The pattern a file name has to match for this handling rule to apply.
  match *regexp.Regexp
  
  // If hide==true, this file will neither be served nor appear in the index.
  hide bool
  
  // If not "", this is a replacement pattern that may include backreferences to
  // the match. After the replacement is applied, the replaced name will be
  // registered as an alias for the file that will be delivered with
  // Content-Encoding: gzip. Has no effect on directories.
  gzip string
}

// Default rules for handling files.
var DefaultHandling = []Handling{
  {match:regexp.MustCompile(`^\.`),          hide:true},
  {match:regexp.MustCompile(`~$`),           hide:true},
  {match:regexp.MustCompile(`%$`),           hide:true},
  {match:regexp.MustCompile(`\.bak$`),       hide:true},
  {match:regexp.MustCompile(`\.svgz$`),      gzip:`.svg`},
  {match:regexp.MustCompile(`\.svg\.gz$`),   gzip:`.svg`},
  {match:regexp.MustCompile(`\.css\.gz$`),   gzip:`.css`},
  {match:regexp.MustCompile(`\.js\.gz$`),    gzip:`.js`},
  {match:regexp.MustCompile(`\.json\.gz$`),  gzip:`.json`},
  {match:regexp.MustCompile(`\.ps\.gz$`),    gzip:`.ps`},
  {match:regexp.MustCompile(`\.pdf\.gz$`),   gzip:`.pdf`},
  {match:regexp.MustCompile(`\.txt\.gz$`),   gzip:`.txt`},
  {match:regexp.MustCompile(`\.xml\.gz$`),   gzip:`.xml`},
  {match:regexp.MustCompile(`\.xhtml\.gz$`), gzip:`.xhtml`},
  {match:regexp.MustCompile(`\.htm\.gz$`),   gzip:`.htm`},
  {match:regexp.MustCompile(`\.html\.gz$`),  gzip:`.html`},
  {match:regexp.MustCompile(`^([^.]+)\.gz$`),gzip:`$1`},
  
  
  {match:regexp.MustCompile(``)}, // catch-all; required to guarantee that a rule matches.
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
  
  var cachesize int64
  cachesize = 10*1024*1024
  if options[CACHE].Count() > 0 {
    if options[CACHE].Last().Value.(int) <= 0 {
      cachesize = 0
    } else {
      cachesize = 1014*1024*int64(options[CACHE].Last().Value.(int))
    }
  }
  
  util.Log(1, "Server root: %v", wd)
  util.Log(1, "Process UID: %v", uid)
  util.Log(1, "Process GID: %v", gid)
  util.Log(1, "HTTP   port: %v", http_port)
  util.Log(1, "Cache  size: %v", cachesize)
  
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
  
                                                  
  fm,err := NewFileManager(wd, DefaultHandling, cachesize)
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


// Each file can have multiple cache entries (plain, gzipped,...)
const NUM_CACHES_PER_FILE = 1

// Handles a directory tree.
type FileManager struct {
  // Maps File.Id to the caches for the corresponding file. 
  cache map[uint64][NUM_CACHES_PER_FILE]interface{}
  
  // The contents of the root directory. Each key is a file name, possibly an alias
  // generated through Handling.gzip.
  tree map[string]*File
  
  // inotify file descriptor used to watch all directories for changes.
  inotify int
  
  // The path of the root directory. If the server is chrooted, this
  // will typically be "/"
  rootdir string
  
  // Whenever tree is accessed, this mutex is used to protect
  // ServerHTTP() from AutoUpdate()
  mutex sync.RWMutex
  
  // The handling rules for file patterns.
  handling []Handling
  
  // If an HTTP request does not have Accept-Encoding: gzip but the
  // requested file is gzipped, it will be extracted into a buffer
  // and served from that, but only if the compressed file size
  // is smaller than this number.
  maxDecodeGzipSize int64
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

// A directory entry managed by FileManager.
type File struct {
  // Stat of the file.
  Info os.FileInfo
  
  // See nextid above.
  Id uint64
  
  // If Info.IsDir() this is a map of the contents of the directory.
  // May include aliases generated through Handling.gzip.
  Contents map[string]*File
  
  // true iff this is an alias for a gzipped file that is to be served
  // with Content-Encoding: gzip.
  Gzip bool
}

/*
  Creates and returns a new FileManager. Does not return until the directory tree has been
  scanned. From then on the directory tree will remain fixed unless you call AutoUpdate().
  
    rootdir: The path of the root of the directory tree
    handling: Special rules for handling certain files
    cachesize: Size of cache in bytes
*/
func NewFileManager(rootdir string, handling []Handling, cachesize int64) (*FileManager, error) {
  fm := &FileManager{tree:map[string]*File{}, inotify:-1, rootdir:rootdir, handling:handling, maxDecodeGzipSize:(cachesize+9)/10}
  err := fm.scan(rootdir, map[string]*File{}, fm.tree)
  if err != nil { return nil, err }
  return fm, nil
}

var empty = map[string]*File{}

// Continuously watches the directory tree of fm and updates the internal
// data if necessary. Never returns.
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
      if fm.handling[hand].match.MatchString(name) { break }
      hand++
    }
    // NOTE: Because fm.handling has a catch-all, it is guaranteed that
    // fm.handling[hand] is valid
    
    n := &File{Info:fi}
    
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
    if !n.Info.IsDir() && fm.handling[hand].gzip != "" {
      alias := fm.handling[hand].match.ReplaceAllString(name, fm.handling[hand].gzip)
      aliases1 = append(aliases1, alias)
      ali_n := *n
      ali_n.Gzip = true
      aliases2 = append(aliases2, &ali_n)
    }
    
    if fm.handling[hand].hide { 
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

func (fm *FileManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
  // turn "" and "." into "/"
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
      if x, ok = dir[name]; !ok {
        break
      }
      where = where + "/" + x.Info.Name()
      if x.Info.IsDir() {
        dir = x.Contents
      } else {
        dir = empty
      }
    }
  }
  fm.mutex.RUnlock()
  
  if !ok || x.Info.IsDir() {
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
  mime := linux.Extension2MIME[path.Ext(clean)]
  if mime == "" { mime = "application/octet-stream" }
  if strings.HasPrefix(mime, "text/") {
    mime += "; charset=UTF-8"
  }
  w.Header().Set("Content-Type", mime)
  
  var serve_content io.ReadSeeker
  serve_content = f
  
  understands_gzip := (x.Info.Size() > fm.maxDecodeGzipSize)
  if x.Gzip {
    for _, aes := range r.Header["Accept-Encoding"] {
      for _, ae := range strings.Split(aes, ",") {
        ae = strings.TrimSpace(ae)
        understands_gzip = understands_gzip || (ae == "gzip")
      }
    }
    
    if !understands_gzip {
      g, err := gzip.NewReader(f)
      if err != nil {
        util.Log(0, "ERROR! un-gzip %v: %v", where, err)
        understands_gzip = true // fall back to sending the original data
      } else {
        defer g.Close()
        data, err := ioutil.ReadAll(g)
        if err != nil {
          util.Log(0, "ERROR! un-gzip %v: %v", where, err)
          understands_gzip = true // fall back to sending the original data
        } else {
          serve_content = bytes.NewReader(data)
        }
      }
    }
    
    if understands_gzip {
      w.Header().Set("Content-Encoding", "gzip")
    }
  }
  
  ce := ""
  if x.Gzip && understands_gzip {
    ce=", Content-Encoding: gzip"
  }
  
  util.Log(0, "%v %v %v (ETag: %v, Content-Type: %v%v)", http.StatusOK, r.Method, r.URL.Path, x.Id, mime, ce)
  http2.ServeContent(w,r,"",x.Info.ModTime(),serve_content)
}


