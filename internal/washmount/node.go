package washmount

import (
	"context"
	"hash/fnv"
	"os"
	"path"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/pkg/sftp"
)

// sftpNode is one inode in the mounted tree. Its remote path is derived from its
// live position in the inode tree (see rpath), not stored — so it follows the
// node across renames. Every operation delegates to the SFTP client on the
// shared root, the single seam where a wash-native backend could later slot in.
type sftpNode struct {
	fs.Inode
	root *sftpRoot
}

// rpath is this node's absolute path on the remote, recomputed from its current
// tree position so it remains valid after the node has been renamed/moved.
func (n *sftpNode) rpath() string {
	return path.Join(n.root.base, n.Path(n.Root()))
}

// Interface assertions: this is the set of operations the SFTP backend supports.
var (
	_ fs.NodeGetattrer  = (*sftpNode)(nil)
	_ fs.NodeSetattrer  = (*sftpNode)(nil)
	_ fs.NodeLookuper   = (*sftpNode)(nil)
	_ fs.NodeReaddirer  = (*sftpNode)(nil)
	_ fs.NodeOpener     = (*sftpNode)(nil)
	_ fs.NodeReader     = (*sftpNode)(nil)
	_ fs.NodeWriter     = (*sftpNode)(nil)
	_ fs.NodeFsyncer    = (*sftpNode)(nil)
	_ fs.NodeFlusher    = (*sftpNode)(nil)
	_ fs.NodeReleaser   = (*sftpNode)(nil)
	_ fs.NodeCreater    = (*sftpNode)(nil)
	_ fs.NodeMkdirer    = (*sftpNode)(nil)
	_ fs.NodeRmdirer    = (*sftpNode)(nil)
	_ fs.NodeUnlinker   = (*sftpNode)(nil)
	_ fs.NodeRenamer    = (*sftpNode)(nil)
	_ fs.NodeReadlinker = (*sftpNode)(nil)
	_ fs.NodeSymlinker  = (*sftpNode)(nil)
)

// idFromPath gives a stable inode number per remote path, so repeated Lookups of
// the same path resolve to one inode (the go-fuse inode table dedups on it)
// rather than a fresh node each time.
func idFromPath(p string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(p))
	return h.Sum64()
}

// newChild builds (or reuses) the child inode for a remote path given its stat.
func (n *sftpNode) newChild(ctx context.Context, p string, fi os.FileInfo) *fs.Inode {
	child := &sftpNode{root: n.root}
	stable := fs.StableAttr{
		Mode: modeBits(fi.Mode()) & syscall.S_IFMT,
		Ino:  idFromPath(p),
	}
	return n.NewInode(ctx, child, stable)
}

func (n *sftpNode) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	var fi os.FileInfo
	if errno := n.root.run(ctx, "lstat", func() (err error) {
		fi, err = n.root.client.Lstat(n.rpath())
		return
	}); errno != 0 {
		return errno
	}
	n.root.fillAttr(fi, &out.Attr)
	return 0
}

func (n *sftpNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := path.Join(n.rpath(), name)
	var fi os.FileInfo
	if errno := n.root.run(ctx, "lstat", func() (err error) {
		fi, err = n.root.client.Lstat(p)
		return
	}); errno != 0 {
		return nil, errno
	}
	n.root.fillAttr(fi, &out.Attr)
	return n.newChild(ctx, p, fi), 0
}

func (n *sftpNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	var infos []os.FileInfo
	if errno := n.root.run(ctx, "readdir", func() (err error) {
		infos, err = n.root.client.ReadDir(n.rpath())
		return
	}); errno != 0 {
		return nil, errno
	}
	entries := make([]fuse.DirEntry, 0, len(infos))
	for _, fi := range infos {
		entries = append(entries, fuse.DirEntry{
			Name: fi.Name(),
			Mode: modeBits(fi.Mode()) & syscall.S_IFMT,
			Ino:  idFromPath(path.Join(n.rpath(), fi.Name())),
		})
	}
	return fs.NewListDirStream(entries), 0
}

func (n *sftpNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	var f *sftp.File
	if errno := n.root.run(ctx, "open", func() (err error) {
		f, err = n.root.client.OpenFile(n.rpath(), int(flags))
		return
	}); errno != 0 {
		return nil, 0, errno
	}
	return &fileHandle{f: f}, 0, 0
}

func (n *sftpNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	h := f.(*fileHandle)
	var num int
	if errno := n.root.run(ctx, "read", func() error {
		h.mu.Lock()
		defer h.mu.Unlock()
		nn, err := h.f.ReadAt(dest, off)
		num = nn
		if err != nil && num > 0 { // short read at EOF is success, not error
			return nil
		}
		return err
	}); errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(dest[:num]), 0
}

func (n *sftpNode) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	h := f.(*fileHandle)
	var num int
	if errno := n.root.run(ctx, "write", func() (err error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		num, err = h.f.WriteAt(data, off)
		return
	}); errno != 0 {
		return 0, errno
	}
	return uint32(num), 0
}

func (n *sftpNode) Fsync(ctx context.Context, f fs.FileHandle, _ uint32) syscall.Errno {
	// SFTP has no fsync request in the base protocol; the data is already on its
	// way to the server. Treat as a no-op success rather than ENOSYS, which some
	// applications treat as fatal.
	return 0
}

func (n *sftpNode) Flush(ctx context.Context, f fs.FileHandle) syscall.Errno {
	return 0
}

func (n *sftpNode) Release(ctx context.Context, f fs.FileHandle) syscall.Errno {
	h := f.(*fileHandle)
	return n.root.run(ctx, "close", func() error {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.f.Close()
	})
}

func (n *sftpNode) Create(ctx context.Context, name string, flags, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	p := path.Join(n.rpath(), name)
	var f *sftp.File
	if errno := n.root.run(ctx, "create", func() (err error) {
		f, err = n.root.client.OpenFile(p, int(flags)|os.O_CREATE)
		return
	}); errno != 0 {
		return nil, nil, 0, errno
	}
	// SFTP OpenFile carries no mode, so set permissions after creation.
	if errno := n.root.run(ctx, "chmod", func() error {
		return n.root.client.Chmod(p, os.FileMode(mode)&os.ModePerm)
	}); errno != 0 {
		f.Close()
		return nil, nil, 0, errno
	}
	var fi os.FileInfo
	if errno := n.root.run(ctx, "lstat", func() (err error) {
		fi, err = n.root.client.Lstat(p)
		return
	}); errno != 0 {
		f.Close()
		return nil, nil, 0, errno
	}
	n.root.fillAttr(fi, &out.Attr)
	return n.newChild(ctx, p, fi), &fileHandle{f: f}, 0, 0
}

func (n *sftpNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := path.Join(n.rpath(), name)
	if errno := n.root.run(ctx, "mkdir", func() error {
		return n.root.client.Mkdir(p)
	}); errno != 0 {
		return nil, errno
	}
	// Best-effort mode; mkdir in SFTP does not take one.
	n.root.run(ctx, "chmod", func() error { return n.root.client.Chmod(p, os.FileMode(mode)&os.ModePerm) })
	var fi os.FileInfo
	if errno := n.root.run(ctx, "lstat", func() (err error) {
		fi, err = n.root.client.Lstat(p)
		return
	}); errno != 0 {
		return nil, errno
	}
	n.root.fillAttr(fi, &out.Attr)
	return n.newChild(ctx, p, fi), 0
}

func (n *sftpNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	return n.root.run(ctx, "rmdir", func() error {
		return n.root.client.RemoveDirectory(path.Join(n.rpath(), name))
	})
}

func (n *sftpNode) Unlink(ctx context.Context, name string) syscall.Errno {
	return n.root.run(ctx, "unlink", func() error {
		return n.root.client.Remove(path.Join(n.rpath(), name))
	})
}

func (n *sftpNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	np, ok := newParent.(*sftpNode)
	if !ok {
		return syscall.EXDEV
	}
	from := path.Join(n.rpath(), name)
	to := path.Join(np.rpath(), newName)
	return n.root.run(ctx, "rename", func() error {
		// PosixRename atomically replaces the target if the server supports the
		// extension; it is the right default for a rename-over-existing.
		return n.root.client.PosixRename(from, to)
	})
}

func (n *sftpNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		if errno := n.root.run(ctx, "truncate", func() error {
			return n.root.client.Truncate(n.rpath(), int64(sz))
		}); errno != 0 {
			return errno
		}
	}
	if mode, ok := in.GetMode(); ok {
		if errno := n.root.run(ctx, "chmod", func() error {
			return n.root.client.Chmod(n.rpath(), os.FileMode(mode)&os.ModePerm)
		}); errno != 0 {
			return errno
		}
	}
	atime, aok := in.GetATime()
	mtime, mok := in.GetMTime()
	if aok || mok {
		if !aok {
			atime = time.Now()
		}
		if !mok {
			mtime = time.Now()
		}
		if errno := n.root.run(ctx, "chtimes", func() error {
			return n.root.client.Chtimes(n.rpath(), atime, mtime)
		}); errno != 0 {
			return errno
		}
	}
	// Report the post-change state.
	var fi os.FileInfo
	if errno := n.root.run(ctx, "lstat", func() (err error) {
		fi, err = n.root.client.Lstat(n.rpath())
		return
	}); errno != 0 {
		return errno
	}
	n.root.fillAttr(fi, &out.Attr)
	return 0
}

func (n *sftpNode) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	var target string
	if errno := n.root.run(ctx, "readlink", func() (err error) {
		target, err = n.root.client.ReadLink(n.rpath())
		return
	}); errno != 0 {
		return nil, errno
	}
	return []byte(target), 0
}

func (n *sftpNode) Symlink(ctx context.Context, target, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := path.Join(n.rpath(), name)
	if errno := n.root.run(ctx, "symlink", func() error {
		return n.root.client.Symlink(target, p)
	}); errno != 0 {
		return nil, errno
	}
	var fi os.FileInfo
	if errno := n.root.run(ctx, "lstat", func() (err error) {
		fi, err = n.root.client.Lstat(p)
		return
	}); errno != 0 {
		return nil, errno
	}
	n.root.fillAttr(fi, &out.Attr)
	return n.newChild(ctx, p, fi), 0
}
