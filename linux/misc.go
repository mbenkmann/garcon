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


package linux

/*
#include <stdlib.h>
#include <sys/types.h>
#include <sys/unistd.h>
#include <pwd.h>
#include <grp.h>
*/
import "C"
import "unsafe"
import "fmt"
import "strconv"

// Returns the numeric UID corresponding to uid.
// If uid is a non-negative number, it is returned.
// Otherwise uid is interpreted as a user name whose
// UID will be returned.
func Getuid(uid string) (int, error) {
  i, err := strconv.Atoi(uid)
  if err == nil && i >= 0 {
    return i, nil
  }
  
  name := C.CString(uid)
  defer C.free(unsafe.Pointer(name))
  var pw C.struct_passwd
  sz := C.size_t(4096)
  buf := C.malloc(sz)
  if buf == nil {
    return -1, fmt.Errorf("malloc failure")
  }
  defer C.free(buf)
  var result *C.struct_passwd
  res, err := C.getpwnam_r(name, &pw, (*C.char)(buf), sz, &result)
  if res != 0 {
    return -1, fmt.Errorf("getpwnam_r failure: %v", err)
  }
  
  if result == nil {
    return -1, fmt.Errorf("User \"%v\" unknown", uid)
  }
  
  return int(pw.pw_uid), nil
}

// Returns the numeric GID corresponding to uid.
// If gid is a non-negative number, it is returned.
// Otherwise gid is interpreted as a group name whose
// GID will be returned.
func Getgid(gid string) (int, error) {
  i, err := strconv.Atoi(gid)
  if err == nil && i >= 0 {
    return i, nil
  }
  
  name := C.CString(gid)
  defer C.free(unsafe.Pointer(name))
  var pw C.struct_group
  sz := C.size_t(4096)
  buf := C.malloc(sz)
  if buf == nil {
    return -1, fmt.Errorf("malloc failure")
  }
  defer C.free(buf)
  var result *C.struct_group
  res, err := C.getgrnam_r(name, &pw, (*C.char)(buf), sz, &result)
  if res != 0 {
    return -1, fmt.Errorf("getgrnam_r failure: %v", err)
  }
  
  if result == nil {
    return -1, fmt.Errorf("Group \"%v\" unknown", gid)
  }
  
  return int(pw.gr_gid), nil
}

func Setuid(uid int) error {
  res, err := C.setuid(C.__uid_t(uid))
  if res == 0 { return nil }
  return fmt.Errorf("setuid(%v): %v", uid, err)
}

func Setgid(gid int) error {
  res, err := C.setgid(C.__gid_t(gid))
  if res == 0 { return nil }
  return fmt.Errorf("setgid(%v): %v", gid, err)
}

/* Does not work for other threads than the caller :-(
// Removes all capabilities from thread tid.
func RemoveAllCapabilities(tid int) error {
  var head C.struct___user_cap_header_struct
  var data [4]C.struct___user_cap_data_struct
  head.version = 0x20071026
  head.pid = C.int(tid)
  res, err := C.capget(&head, C.cap_user_data_t(&(data[0])))
  if res < 0 { return fmt.Errorf("capget(): %v", err) }
  fmt.Printf("%#v\n",data)
  for i := range data {
    data[i].effective = 0
    data[i].inheritable = 0
    data[i].permitted = 0
  }
  res, err = C.capset(&head, C.cap_user_data_t(&(data[0])))
  if res < 0 { return fmt.Errorf("capset(%v): %v", tid, err) }
  
  return nil
}
*/
