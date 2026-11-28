// Package exportfile creates private export files in an owner-selected directory.
//
//persona:layer application
package exportfile

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrExists means the destination name is already taken.
var ErrExists = errors.New("export destination already exists")

// Destination holds the output directory open until publication or cancellation.
type Destination struct {
	root *os.Root
	name string
}

// Open checks the name before any export data is fetched. Call Close when finished.
func Open(path string) (*Destination, error) {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	if name == "" || name == "." || name == ".." {
		return nil, fs.ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	if _, err := root.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
		root.Close()
		if err == nil {
			err = ErrExists
		}
		return nil, err
	}
	return &Destination{root, name}, nil
}

// Close releases the open directory.
func (d *Destination) Close() error {
	return d.root.Close()
}

// Publish links a synced private inode into place. The link is the only publication
// point and cannot replace an existing file, even if another writer won the race.
func (d *Destination) Publish(ctx context.Context, write func(io.Writer) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary := ".persona-export-" + rand.Text()
	file, err := d.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer d.root.Remove(temporary)
	if err := write(file); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.root.Link(temporary, d.name); errors.Is(err, fs.ErrExist) {
		return ErrExists
	} else {
		return err
	}
}
