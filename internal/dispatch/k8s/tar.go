package k8s

import (
	"archive/tar"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// tarDir streams dir as a tar archive. It is the remote CopyIn: no volume is shared with the
// sandbox, the bytes go over the runtime's file endpoint.
func tarDir(dir string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil || rel == "." {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return nil // sockets, devices and pipes stay out
			}
			link := ""
			if info.Mode()&os.ModeSymlink != 0 {
				if link, err = os.Readlink(p); err != nil {
					return err
				}
			}
			h, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			h.Name = filepath.ToSlash(rel)
			h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
			if err := tw.WriteHeader(h); err != nil || !info.Mode().IsRegular() {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			_, err = io.Copy(tw, f)
			return err
		})
		if err == nil {
			err = tw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	return pr
}
