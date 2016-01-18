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
         "time"
         "regexp"
         "strconv"
         "syscall"
         "github.com/mbenkmann/golib/argv"
         "github.com/mbenkmann/golib/util"
         
         "../linux"
         "../fs"
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
{ 0, 0, "", "",argv.ArgUnknown, "\f" },
{ UNKNOWN, 1, "", "",     argv.ArgUnknown, `CONTENT-ENCODING: GZIP

To reduce bandwidth usage and improve page load times, files in text-based formats like HTML and CSS can be compressed with gzip. When Garçon sees a file with an extension from the following table, in addition to serving it under its actual name, Garçon will serve it under an alternative name. If the alternative name is requested by a client without gzip support, Garçon will decompress it on the fly. Otherwise Garçon will send the gzipped data with an added "Content-Encoding: gzip" header. If an actual file with the translated name exists, that will take precedence.

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


// Default rules for handling files.
var DefaultHandling = []fs.Handling{
  {Match:regexp.MustCompile(`^\.`),          Hide:true},
  {Match:regexp.MustCompile(`~$`),           Hide:true},
  {Match:regexp.MustCompile(`%$`),           Hide:true},
  {Match:regexp.MustCompile(`\.bak$`),       Hide:true},
  {Match:regexp.MustCompile(`\.svgz$`),      Gzip:`.svg`},
  {Match:regexp.MustCompile(`\.svg\.gz$`),   Gzip:`.svg`},
  {Match:regexp.MustCompile(`\.css\.gz$`),   Gzip:`.css`},
  {Match:regexp.MustCompile(`\.js\.gz$`),    Gzip:`.js`},
  {Match:regexp.MustCompile(`\.json\.gz$`),  Gzip:`.json`},
  {Match:regexp.MustCompile(`\.ps\.gz$`),    Gzip:`.ps`},
  {Match:regexp.MustCompile(`\.pdf\.gz$`),   Gzip:`.pdf`},
  {Match:regexp.MustCompile(`\.txt\.gz$`),   Gzip:`.txt`},
  {Match:regexp.MustCompile(`\.xml\.gz$`),   Gzip:`.xml`},
  {Match:regexp.MustCompile(`\.xhtml\.gz$`), Gzip:`.xhtml`},
  {Match:regexp.MustCompile(`\.htm\.gz$`),   Gzip:`.htm`},
  {Match:regexp.MustCompile(`\.html\.gz$`),  Gzip:`.html`},
  {Match:regexp.MustCompile(`^([^.]+)\.gz$`),Gzip:`$1`},
  
  
  {Match:regexp.MustCompile(``)}, // catch-all; required to guarantee that a rule matches.
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
  
                                                  
  fm,err := fs.NewFileManager(wd, DefaultHandling)
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

