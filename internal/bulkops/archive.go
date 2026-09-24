// Archive create + extract, as queued jobs.
//
// Both directions are long-running walks over a tree, which is exactly
// what the bulk queue exists for: "Extract here" on a 2 GB tarball has
// to show progress and be cancellable, and so does compressing a big
// folder. So they are ops (OpExtract, OpCompress) rather than something
// fm does inline in its BE.
//
// Formats: zip, tar, tar.gz/tgz. .tar.xz is deliberately absent — the
// standard library has no xz codec and this package takes no third-party
// dependency for one container.
//
// Safety: every path out of an archive is resolved through safeJoin,
// which is the zip-slip guard. An entry named "../../etc/cron.d/x", an
// absolute "/etc/passwd", or a symlink pointing out of the destination
// are all refused — the first two by rejecting the name, the last by
// resolving the link target before creating it. Refusals are counted and
// reported, never silently applied.
package bulkops

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrUnsupportedArchive is returned for a container this package cannot
// read or write (notably .tar.xz — see the package comment).
var ErrUnsupportedArchive = errors.New("unsupported archive format")

// archiveFormat is the container a name implies.
type archiveFormat string

const (
	formatZip   archiveFormat = "zip"
	formatTar   archiveFormat = "tar"
	formatTarGz archiveFormat = "tar.gz"
)

// ArchiveFormatOf classifies a file NAME (not its bytes). Unknown or
// unsupported suffixes return false.
func ArchiveFormatOf(name string) (archiveFormat, bool) {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".zip"):
		return formatZip, true
	case strings.HasSuffix(l, ".tar.gz"), strings.HasSuffix(l, ".tgz"):
		return formatTarGz, true
	case strings.HasSuffix(l, ".tar"):
		return formatTar, true
	}
	return "", false
}

// IsExtractable reports whether "Extract here" should be offered for a
// file name. The FE asks this through fm; keeping it here means the menu
// and the worker can never disagree about what is supported.
func IsExtractable(name string) bool {
	_, ok := ArchiveFormatOf(name)
	return ok
}

// ---- the zip-slip guard ----

// safeJoin resolves an archive member name against dest and refuses
// anything that would land outside it: an absolute name, a name that
// climbs out with "..", or one whose cleaned form escapes. The returned
// path is always dest or below.
func safeJoin(dest, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("archive entry has an empty name")
	}
	// Archive members use forward slashes regardless of the writing OS,
	// and a Windows-made archive may carry backslashes.
	slash := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(slash, "/") {
		return "", fmt.Errorf("archive entry %q is an absolute path", name)
	}
	// REFUSE a climbing name rather than sanitising it. Rewriting
	// "../etc/passwd" to "etc/passwd" would extract an entry the archive
	// never described, under a name the user never saw — a silent lie
	// about what the archive contained.
	clean := path.Clean(slash)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	out := filepath.Join(dest, filepath.FromSlash(clean))
	if out != dest && !strings.HasPrefix(out, dest+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return out, nil
}

// safeSymlinkTarget checks that a symlink placed at linkPath pointing at
// target would resolve inside dest. An absolute target is refused
// outright; a relative one is resolved against the link's own directory.
func safeSymlinkTarget(dest, linkPath, target string) error {
	if target == "" {
		return fmt.Errorf("symlink has an empty target")
	}
	if filepath.IsAbs(target) || strings.HasPrefix(target, "/") {
		return fmt.Errorf("symlink target %q is absolute", target)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(linkPath), filepath.FromSlash(target)))
	if resolved != dest && !strings.HasPrefix(resolved, dest+string(os.PathSeparator)) {
		return fmt.Errorf("symlink target %q escapes the destination", target)
	}
	return nil
}

// ---- extract ----

// ExtractStats is what an extraction did.
type ExtractStats struct {
	Files    int
	Dirs     int
	Symlinks int
	Refused  int // entries the zip-slip guard rejected
	Skipped  int // entry kinds not extracted (devices, fifos, …)
}

// Extract unpacks the archive at src into dest (which is created if
// missing). onEntry is called once per extracted entry for progress;
// cancelled is polled between entries. The first guard refusal does NOT
// abort the extraction — the remaining entries are still safe and the
// count comes back in ExtractStats — but a refusal makes the job report
// an error so the user is told.
func Extract(src, dest string, cancelled func() bool, onEntry func()) (ExtractStats, error) {
	format, ok := ArchiveFormatOf(src)
	if !ok {
		return ExtractStats{}, fmt.Errorf("%w: %s", ErrUnsupportedArchive, filepath.Base(src))
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return ExtractStats{}, err
	}
	if err := os.MkdirAll(destAbs, 0o755); err != nil {
		return ExtractStats{}, err
	}
	if format == formatZip {
		return extractZip(src, destAbs, cancelled, onEntry)
	}
	return extractTar(src, destAbs, format, cancelled, onEntry)
}

// CountEntries is the pre-pass that gives a job an accurate Total. It
// reads the archive's index (zip) or streams its headers (tar), so it is
// cheap for zip and one extra decompression for tar.gz.
func CountEntries(src string) (int, error) {
	format, ok := ArchiveFormatOf(src)
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrUnsupportedArchive, filepath.Base(src))
	}
	if format == formatZip {
		zr, err := zip.OpenReader(src)
		if err != nil {
			return 0, err
		}
		defer zr.Close()
		return len(zr.File), nil
	}
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r, closer, err := tarReaderFor(f, format)
	if err != nil {
		return 0, err
	}
	defer closer()
	n := 0
	for {
		if _, err := r.Next(); err != nil {
			if errors.Is(err, io.EOF) {
				return n, nil
			}
			return n, err
		}
		n++
	}
}

func extractZip(src, dest string, cancelled func() bool, onEntry func()) (ExtractStats, error) {
	var st ExtractStats
	zr, err := zip.OpenReader(src)
	if err != nil {
		return st, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if cancelled != nil && cancelled() {
			return st, nil
		}
		out, err := safeJoin(dest, f.Name)
		if err != nil {
			st.Refused++
			continue
		}
		mode := f.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return st, err
			}
			target, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return st, err
			}
			if err := writeSymlink(dest, out, string(target)); err != nil {
				st.Refused++
				continue
			}
			st.Symlinks++
		case f.FileInfo().IsDir():
			if err := os.MkdirAll(out, dirPerm(mode)); err != nil {
				return st, err
			}
			st.Dirs++
		case mode.IsRegular():
			rc, err := f.Open()
			if err != nil {
				return st, err
			}
			err = writeRegular(out, rc, mode.Perm())
			rc.Close()
			if err != nil {
				return st, err
			}
			st.Files++
		default:
			st.Skipped++
		}
		if onEntry != nil {
			onEntry()
		}
	}
	return st, nil
}

func tarReaderFor(f *os.File, format archiveFormat) (*tar.Reader, func(), error) {
	if format == formatTarGz {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, nil, err
		}
		return tar.NewReader(gz), func() { gz.Close() }, nil
	}
	return tar.NewReader(f), func() {}, nil
}

func extractTar(src, dest string, format archiveFormat, cancelled func() bool, onEntry func()) (ExtractStats, error) {
	var st ExtractStats
	f, err := os.Open(src)
	if err != nil {
		return st, err
	}
	defer f.Close()
	tr, closer, err := tarReaderFor(f, format)
	if err != nil {
		return st, err
	}
	defer closer()
	for {
		if cancelled != nil && cancelled() {
			return st, nil
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return st, nil
		}
		if err != nil {
			return st, err
		}
		out, err := safeJoin(dest, hdr.Name)
		if err != nil {
			st.Refused++
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, dirPerm(hdr.FileInfo().Mode())); err != nil {
				return st, err
			}
			st.Dirs++
		case tar.TypeSymlink:
			if err := writeSymlink(dest, out, hdr.Linkname); err != nil {
				st.Refused++
				continue
			}
			st.Symlinks++
		case tar.TypeReg:
			if err := writeRegular(out, tr, hdr.FileInfo().Mode().Perm()); err != nil {
				return st, err
			}
			st.Files++
		default:
			// Hard links, devices, fifos: not something a file manager's
			// "Extract here" should be creating.
			st.Skipped++
		}
		if onEntry != nil {
			onEntry()
		}
	}
}

func dirPerm(mode fs.FileMode) fs.FileMode {
	if p := mode.Perm(); p != 0 {
		return p | 0o700 // always keep ourselves able to descend
	}
	return 0o755
}

func writeRegular(out string, r io.Reader, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeSymlink(dest, out, target string) error {
	if err := safeSymlinkTarget(dest, out, target); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	_ = os.Remove(out)
	return os.Symlink(target, out)
}

// ---- create ----

// WriteArchive writes `paths` (files and/or dirs, walked recursively)
// into w in the container implied by `name`. Each source keeps its own
// base name as the archive prefix, so archiving the folder "proj" yields
// "proj/…" inside. onEntry fires per member written.
//
// Symlinks are stored AS symlinks (never followed — a link out of the
// tree must not silently pull its target in), and directories get their
// own entries so an empty one survives the round trip. Devices, sockets
// and fifos are skipped: they do not survive an archive meaningfully.
func WriteArchive(w io.Writer, name string, paths []string, onEntry func()) error {
	format, ok := ArchiveFormatOf(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedArchive, name)
	}
	if format == formatZip {
		return writeZipTo(w, paths, onEntry)
	}
	return writeTarTo(w, paths, format, onEntry)
}

// archiveMember is one thing to write: its path on disk, its name inside
// the archive, and its Lstat info.
type archiveMember struct {
	abs  string
	name string
	info os.FileInfo
}

// walkMembers expands `paths` into the members to write, in a stable
// order. Lstat throughout: a symlink is a member in its own right.
func walkMembers(paths []string) ([]archiveMember, error) {
	var out []archiveMember
	for _, abs := range paths {
		fi, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			out = append(out, archiveMember{abs: abs, name: filepath.Base(abs), info: fi})
			continue
		}
		// Anchor at the parent so the selected dir's own name is included.
		base := filepath.Dir(abs)
		err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return err
			}
			out = append(out, archiveMember{abs: p, name: filepath.ToSlash(rel), info: info})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func writeZipTo(w io.Writer, paths []string, onEntry func()) error {
	members, err := walkMembers(paths)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	for _, m := range members {
		if err := addZipMember(zw, m); err != nil {
			return err
		}
		if onEntry != nil {
			onEntry()
		}
	}
	return zw.Close()
}

func addZipMember(zw *zip.Writer, m archiveMember) error {
	hdr, err := zip.FileInfoHeader(m.info)
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(m.name)
	switch {
	case m.info.IsDir():
		// The trailing slash IS how a zip records a directory — without
		// it an empty folder simply isn't in the archive.
		hdr.Name += "/"
		_, err := zw.CreateHeader(hdr)
		return err
	case m.info.Mode()&os.ModeSymlink != 0:
		// Info-ZIP's convention: the symlink bit in the external
		// attributes, target as the entry's body.
		target, err := os.Readlink(m.abs)
		if err != nil {
			return err
		}
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.WriteString(dst, target)
		return err
	case m.info.Mode().IsRegular():
		hdr.Method = zip.Deflate
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(m.abs)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(dst, f)
		return err
	}
	return nil // device / socket / fifo: nothing meaningful to store
}

func writeTarTo(w io.Writer, paths []string, format archiveFormat, onEntry func()) error {
	members, err := walkMembers(paths)
	if err != nil {
		return err
	}
	var gz *gzip.Writer
	sink := w
	if format == formatTarGz {
		gz = gzip.NewWriter(w)
		sink = gz
	}
	tw := tar.NewWriter(sink)
	for _, m := range members {
		if err := addTarMember(tw, m); err != nil {
			return err
		}
		if onEntry != nil {
			onEntry()
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}

func addTarMember(tw *tar.Writer, m archiveMember) error {
	link := ""
	if m.info.Mode()&os.ModeSymlink != 0 {
		var err error
		if link, err = os.Readlink(m.abs); err != nil {
			return err
		}
	}
	hdr, err := tar.FileInfoHeader(m.info, link)
	if err != nil {
		return err
	}
	hdr.Name = filepath.ToSlash(m.name)
	if m.info.IsDir() {
		hdr.Name += "/"
	}
	if !m.info.Mode().IsRegular() {
		hdr.Size = 0
	}
	switch {
	case m.info.IsDir(), m.info.Mode()&os.ModeSymlink != 0:
		return tw.WriteHeader(hdr)
	case m.info.Mode().IsRegular():
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(m.abs)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}
	return nil
}
