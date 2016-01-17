type Cache struct {
}

/*
  If the cache contains the file with the given id, it
  is returned. Otherwise returns nil.
*/
func (c *Cache) Get(id uint64) io.ReadSeeker {
  return nil
}

/*
  If f is acceptable for the cache as judged by x, f is read completely
  into a buffer and stored in the cache under x.Id,
  potentially purging older entries from the cache. The new buffer is then returned.
  If f is not acceptable for the cache (typically because it is too large
  or because x's data is already in memory in the File.Data field),
  then f is returned.
*/
func (c *Cache) Put(x *File, f io.Reader, gzipped bool) io.Reader {
  // NOTE: Use golib's bytes.Buffer to make sure that memory is returned to the OS when
  // old entries are purged from the cache. Also take care to purge old entries first
  // before reading in the new data, so that the freed memory can be reused.
  
  
  return f
}

