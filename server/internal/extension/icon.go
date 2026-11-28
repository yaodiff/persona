package extension

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"image/png"
	"io"
	"os"
	"strings"
)

const (
	IconPath = "assets/icon.png"
	IconMost = 2 << 20
	IconMin  = 256
	IconMax  = 4096
)

var ErrIconUnavailable = errors.New("extension artwork unavailable")

func ValidIconPath(path string) bool { return path == IconPath }

func CheckIcon(data []byte) error {
	if len(data) == 0 || len(data) > IconMost {
		return ErrIconUnavailable
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != cfg.Height || cfg.Width < IconMin || cfg.Width > IconMax {
		return ErrIconUnavailable
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return ErrIconUnavailable
	}
	return nil
}

func iconRoot(dir string) (*os.Root, error) {
	if dir == "" {
		return nil, ErrIconUnavailable
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrIconUnavailable
	}
	assets, err := r.Lstat("assets")
	if err != nil || !assets.IsDir() || assets.Mode()&os.ModeSymlink != 0 {
		r.Close()
		return nil, ErrIconUnavailable
	}
	fi, err := r.Lstat(IconPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() <= 0 || fi.Size() > IconMost {
		r.Close()
		return nil, ErrIconUnavailable
	}
	return r, nil
}

func HasIcon(dir string) bool {
	r, err := iconRoot(dir)
	if err != nil {
		return false
	}
	r.Close()
	return true
}

func ReadIcon(dir string) ([]byte, error) {
	r, err := iconRoot(dir)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(IconPath)
	if err != nil {
		return nil, ErrIconUnavailable
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, ErrIconUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(f, IconMost+1))
	if err != nil || CheckIcon(data) != nil {
		return nil, ErrIconUnavailable
	}
	return data, nil
}

// boundedZip checks central-directory bounds before archive/zip allocates its file list.
func boundedZip(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < 22 || fi.Size() > PackageMost {
		return false
	}
	start := max(int64(0), fi.Size()-65557)
	tail, err := io.ReadAll(io.NewSectionReader(f, start, fi.Size()-start))
	if err != nil {
		return false
	}
	at := bytes.LastIndex(tail, []byte{'P', 'K', 5, 6})
	if at < 0 || len(tail)-at < 22 {
		return false
	}
	end := tail[at:]
	le := binary.LittleEndian
	count := int(le.Uint16(end[10:12]))
	size, offset := int64(le.Uint32(end[12:16])), int64(le.Uint32(end[16:20]))
	if int(le.Uint16(end[20:22])) != len(end)-22 || le.Uint16(end[4:6]) != 0 || le.Uint16(end[6:8]) != 0 || int(le.Uint16(end[8:10])) != count || count == 0 || count > PackageFiles || size > 8<<20 || offset+size != start+int64(at) {
		return false
	}
	var header [46]byte
	seen := 0
	for pos := offset; pos < offset+size; {
		if seen >= count {
			return false
		}
		if _, err := f.ReadAt(header[:], pos); err != nil || !bytes.Equal(header[:4], []byte{'P', 'K', 1, 2}) {
			return false
		}
		step := int64(46) + int64(le.Uint16(header[28:30])) + int64(le.Uint16(header[30:32])) + int64(le.Uint16(header[32:34]))
		if pos+step > offset+size {
			return false
		}
		pos += step
		seen++
	}
	return seen == count
}

// PackageIcon scans a verified archive without extracting or running any code.
func PackageIcon(file, name, id string) ([]byte, error) {
	if !ValidID(id) {
		return nil, ErrIconUnavailable
	}
	var data []byte
	var b budget
	read := func(path string, size int64, mode os.FileMode, r io.Reader) error {
		if strings.HasPrefix(path, "/") {
			return ErrIconUnavailable
		}
		clean := strings.TrimPrefix(path, "./")
		clean = strings.TrimSuffix(clean, "/")
		if clean == "" || clean == "." {
			if !mode.IsDir() || size != 0 || b.take(0) != nil {
				return ErrIconUnavailable
			}
			return nil
		}
		if !inside(clean) || size < 0 || b.take(size) != nil {
			return ErrIconUnavailable
		}
		if macMetadata(clean) {
			return nil
		}
		isAssets := clean == "assets" || clean == id+"/assets" || clean == id
		isIcon := clean == IconPath || clean == id+"/"+IconPath
		if isAssets && !mode.IsDir() {
			return ErrIconUnavailable
		}
		if !isIcon {
			return nil
		}
		if data != nil || !mode.IsRegular() || size <= 0 || size > IconMost {
			return ErrIconUnavailable
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(r, IconMost+1))
		return err
	}
	switch n := strings.ToLower(name); {
	case strings.HasSuffix(n, ".zip"):
		if !boundedZip(file) {
			return nil, ErrIconUnavailable
		}
		zr, err := zip.OpenReader(file)
		if err != nil {
			return nil, ErrIconUnavailable
		}
		defer zr.Close()
		for _, f := range zr.File {
			if f.UncompressedSize64 > PackageMost {
				return nil, ErrIconUnavailable
			}
			reader, err := f.Open()
			if err != nil {
				return nil, ErrIconUnavailable
			}
			err = read(f.Name, int64(f.UncompressedSize64), f.Mode(), reader)
			reader.Close()
			if err != nil {
				return nil, ErrIconUnavailable
			}
		}
	case strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".tgz"):
		f, err := os.Open(file)
		if err != nil {
			return nil, ErrIconUnavailable
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, ErrIconUnavailable
		}
		defer gz.Close()
		tr := tar.NewReader(io.LimitReader(gz, PackageMost+PackageFiles*1024))
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil || read(h.Name, h.Size, h.FileInfo().Mode(), tr) != nil {
				return nil, ErrIconUnavailable
			}
		}
	default:
		return nil, ErrIconUnavailable
	}
	if CheckIcon(data) != nil {
		return nil, ErrIconUnavailable
	}
	return data, nil
}
