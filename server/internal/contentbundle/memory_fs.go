package contentbundle

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

// memoryFS is a read-only file system over verified bytes. Directories are
// implied by file paths, entries list in name order, and every file reports
// mode 0444 and a zero time, so the files a Run loads are exactly the bytes
// the lock pins.
type memoryFS struct {
	files map[string][]byte
	dirs  map[string][]fs.DirEntry
}

var (
	_ fs.ReadDirFS  = (*memoryFS)(nil)
	_ fs.ReadFileFS = (*memoryFS)(nil)
	_ fs.StatFS     = (*memoryFS)(nil)
)

func newMemoryFS(files map[string][]byte) *memoryFS {
	memory := &memoryFS{files: make(map[string][]byte, len(files)), dirs: map[string][]fs.DirEntry{".": nil}}
	children := map[string]map[string]fs.DirEntry{".": {}}
	for name, contents := range files {
		memory.files[name] = contents
		child := name
		entry := fs.FileInfoToDirEntry(memoryInfo{name: path.Base(name), size: int64(len(contents))})
		for {
			parent := path.Dir(child)
			if children[parent] == nil {
				children[parent] = map[string]fs.DirEntry{}
			}
			children[parent][path.Base(child)] = entry
			if parent == "." {
				break
			}
			entry = fs.FileInfoToDirEntry(memoryInfo{name: path.Base(parent), dir: true})
			child = parent
		}
	}
	for dir, entries := range children {
		sorted := make([]fs.DirEntry, 0, len(entries))
		for _, entry := range entries {
			sorted = append(sorted, entry)
		}
		slices.SortFunc(
			sorted,
			func(left, right fs.DirEntry) int { return strings.Compare(left.Name(), right.Name()) },
		)
		memory.dirs[dir] = sorted
	}
	return memory
}

func (memory *memoryFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if contents, ok := memory.files[name]; ok {
		return &memoryFile{
			info:   memoryInfo{name: path.Base(name), size: int64(len(contents))},
			reader: bytes.NewReader(contents),
		}, nil
	}
	if entries, ok := memory.dirs[name]; ok {
		return &memoryDir{info: memoryInfo{name: path.Base(name), dir: true}, entries: entries}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (memory *memoryFS) ReadFile(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrInvalid}
	}
	contents, ok := memory.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "readfile", Path: name, Err: fs.ErrNotExist}
	}
	return slices.Clone(contents), nil
}

func (memory *memoryFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	entries, ok := memory.dirs[name]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return slices.Clone(entries), nil
}

func (memory *memoryFS) Stat(name string) (fs.FileInfo, error) {
	file, err := memory.Open(name)
	if err != nil {
		return nil, err
	}
	return file.Stat()
}

type memoryInfo struct {
	name string
	size int64
	dir  bool
}

func (info memoryInfo) Name() string       { return info.name }
func (info memoryInfo) Size() int64        { return info.size }
func (info memoryInfo) ModTime() time.Time { return time.Time{} }
func (info memoryInfo) IsDir() bool        { return info.dir }
func (info memoryInfo) Sys() any           { return nil }

func (info memoryInfo) Mode() fs.FileMode {
	if info.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

type memoryFile struct {
	info   memoryInfo
	reader *bytes.Reader
}

func (file *memoryFile) Stat() (fs.FileInfo, error) { return file.info, nil }
func (file *memoryFile) Read(buffer []byte) (int, error) {
	return file.reader.Read(buffer)
}

func (file *memoryFile) Seek(offset int64, whence int) (int64, error) {
	return file.reader.Seek(offset, whence)
}

func (file *memoryFile) ReadAt(buffer []byte, offset int64) (int, error) {
	return file.reader.ReadAt(buffer, offset)
}
func (file *memoryFile) Close() error { return nil }

type memoryDir struct {
	info    memoryInfo
	entries []fs.DirEntry
	offset  int
}

func (dir *memoryDir) Stat() (fs.FileInfo, error) { return dir.info, nil }
func (dir *memoryDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: dir.info.name, Err: fs.ErrInvalid}
}
func (dir *memoryDir) Close() error { return nil }

// ReadDir follows fs.ReadDirFile: n <= 0 returns every remaining entry, and a
// positive n returns io.EOF once the directory is exhausted.
func (dir *memoryDir) ReadDir(count int) ([]fs.DirEntry, error) {
	remaining := dir.entries[dir.offset:]
	if count <= 0 {
		dir.offset = len(dir.entries)
		return slices.Clone(remaining), nil
	}
	if len(remaining) == 0 {
		return nil, io.EOF
	}
	count = min(count, len(remaining))
	dir.offset += count
	return slices.Clone(remaining[:count]), nil
}
