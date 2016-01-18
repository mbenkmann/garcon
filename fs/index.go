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
         "os"
         "time"
         
         "github.com/mbenkmann/golib/util"
         
         "../embedded"
       )

var defaultIndex = &File{
    Info: &FileInfo{"index.xhtml",int64(len(embedded.DefaultIndex)),os.ModeDir|0777,time.Now(),false},
    Id:0,
    Contents:nil,
    Gzip:false,
    Data:embedded.DefaultIndex,
}

/*
  Where necessary this function adds index.html files to the directory tree starting at root.
  Because this requires reading, ungzipping, parsing,... of files, a variety of errors
  may occur from OS-level errors (e.g. permission denied) to XML parse errors. These
  will be logged but will not cause the function to abort.
  
  If root does not contain title information (e.g. from an index.html) the
  provided title will be used if necessary. For other directories in the
  directory tree this defaults to the directory name.
*/
func AddIndexes(root map[string]*File, title string) {
  tree := buildMetaIndex(root,title)
  generateIndexes(tree)
}

// Walks through the meta-index tree (as built by buildMetaIndex())
// and adds index.html files to all directories where necessary.
func generateIndexes(tree [][]indexInfo) {
}

// Takes the directory tree starting at root and builds a tree of indexInfo
// structures (see indexInfo for details on how the tree is stored in the
// returned [][]indexInfo) that contains the necessary information for
// generating the index.html files.
func buildMetaIndex(root map[string]*File, title string) [][]indexInfo {
  tree := make([][]indexInfo,1)
  tree[0] = make([]indexInfo,3) // 3 because we have a dummy entry before and after root
  tree[0][1].files = root
  tree[0][1].title = title
  level := 0
  for len(tree[level]) > 2 { // We stop when a level consists only of the 2 dummy entries every level has
    level++
    tree = append(tree, make([]indexInfo,1)) // every level starts with a dummy entry
    
    for i := 1; i < len(tree[level-1])-1; i++ {
      parent := &tree[level-1][i]
      
      // if the directory turns out not to have any children, this will be reset to 0 later
      parent.first_child = len(tree[level])
      
      // default values for navbar_root and navbar_type are inherited from parent
      if level > 1 {
        parent.navbar_root = tree[level-2][parent.parent].navbar_root - 1
        parent.navbar_type = tree[level-2][parent.parent].navbar_type
      }
      
      // default value for indexfile. Will be overridden if something better is found.
      parent.indexfile = defaultIndex
      indexfile_prio := 0
      
      indexpic_prio := 0
      
      for name, x := range parent.files {
        if x.Info.IsDir() {
          tree[level] = append(tree[level], indexInfo{parent:i, files:x.Contents, title:name})
        }
        
        switch name {
          case "index.css":   err := getDirectivesFromStyles(x, parent)
                              if err != nil {
                                util.Log(0, "ERROR! %v: %v", parent.indexfile, err)
                              }
          case "index.html":  if indexfile_prio < 2 {
                                indexfile_prio = 2
                                parent.indexfile = x
                                parent.index_verbatim = true
                              }
          case "index.xhtml": if indexfile_prio < 1 {
                                indexfile_prio = 1
                                parent.indexfile = x
                              }
          case "index.jpeg":  if indexpic_prio < 4 {
                                indexpic_prio = 4
                                parent.indexpic = x
                              }
          case "index.jpg":   if indexpic_prio < 3 {
                                indexpic_prio = 3
                                parent.indexpic = x
                              }
          case "index.png":   if indexpic_prio < 2 {
                                indexpic_prio = 2
                                parent.indexpic = x
                              }
          case "index.gif":   if indexpic_prio < 1 {
                                indexpic_prio = 1
                                parent.indexpic = x
                              }
        }
      }
      
      // Parse directives from indexfile if it is something other than DefaultIndex.
      if indexfile_prio > 0 {
        err := getDirectivesFromXHTMLHeader(parent.indexfile, parent)
        if err != nil {
          util.Log(0, "ERROR! %v: %v", parent.indexfile, err)
        }
      }
      
      // If we haven't actually added any children, reset first_child to 0
      if len(tree[level]) == parent.first_child {
        parent.first_child = 0
      }
    }
    
    tree[level] = append(tree[level], indexInfo{}) // every level ends with a dummy entry
  }
  
  return tree
}

const AUTO = 0
const NAVBAR_SHALLOW = 1
const NAVBAR_DEEP = 2

// Stores information used to create a single index.html.
type indexInfo struct {
  // indexInfos are linked together to form a tree stored in a [][]indexInfo
  // by means of parent and first_child.
  // If tree[y][x] is an indexInfo I, then tree[y-1][parent] is the parent.
  // If parent==0, this is the root of the tree and has no parent.
  parent int
  
  // If tree[y][x] is an indexInfo I, then tree[y+1][first_child] is the
  // first child (i.e. the indexInfo corresponding to the first subdirectory).
  // If first_child==0 there are no children.
  first_child int
  
  // The files and subdirectories contained in the directory this indexInfo is for.
  files map[string]*File
  
  // The file that provides the base information from which to generate index.html.
  // See index_verbatim below.
  indexfile *File
  
  // If true, indexfile does not need to be processed. It has been
  // provided as index.html directly. If false, indexfile is
  // an xhtml file that may contain processing instructions
  // for garçon to handle in order to produce the index.html file.
  index_verbatim bool

  // If non-nil, this is a picture that somehow represents this directory.
  indexpic *File
  
  // If 0, this directory is the root for navbar generation.
  // If -1, the parent directory is the root. If -2 the parent dir's parent dir is
  // the root and so on.
  // If +1, then each subdir of this dir is its own navbar root.
  navbar_root int
  
  // AUTO, NAVBAR_SHALLOW or NAVBAR_DEEP
  // The type of navbar to include in index.html if requested by indexfile
  // (if index_verbatim==false).
  navbar_type int
  
  // The description of this directory (if provided by indexfile).
  description string
  
  // The title of this directory, either provided by indexfile or taken
  // from the name of the directory.
  title string
}

/*
  Parses the CSS file x and extracts Garçon directives from it that
  concern index generation and stores them in info.
  
  ERRORS: CSS parse errors will not be reported in the return code
          unless they occur in a section that has been identified
          as being meant for Garçon to interpret.
*/
func getDirectivesFromStyles(x *File, info *indexInfo) error {
  r,_,err := x.GetStream(false)
  if err != nil { return err }
  
  if r == nil { panic("foo") }
  
  return nil
}

/*
  Parses the <head> part of (X)HTML file x and extracts Garçon directives
  from it that concern index generation and stores them in info.
  While this function uses an XML parser it will usually work for
  plain HTML files, too, because any XML-invalidities will not surface
  until after the end of the <head> section.
  
  NOTE: This function will actually parse until it sees <body>. The
  existence of an actual <head> tag is not required, nor is the
  existence of <html>.
  
  ERRORS: XML parse errors will not be reported in the return code.
*/
func getDirectivesFromXHTMLHeader(x *File, info *indexInfo) error {
  r,_,err := x.GetStream(false)
  if err != nil { return err }
  
  if r == nil { panic("foo") }
  
  return nil
}

