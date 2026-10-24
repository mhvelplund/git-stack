package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Store reads and writes the metadata file in the git common dir.
type Store struct {
	Dir string // git common dir
}

func (st Store) path() string     { return filepath.Join(st.Dir, "git-stack") }
func (st Store) lockPath() string { return filepath.Join(st.Dir, "git-stack.lock") }

// Load reads the metadata file. A missing file is an empty File.
func (st Store) Load() (*File, error) {
	data, err := ReadStateFile(st.path())
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: FormatVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", st.path(), err)
	}
	if f.Version > FormatVersion {
		return nil, fmt.Errorf("%s was written by a newer git-stack (format %d)", st.path(), f.Version)
	}
	return &f, nil
}

// Save writes f atomically.
func (st Store) Save(f *File) error {
	f.Version = FormatVersion
	return st.writeJSON(st.path(), f)
}

// writeJSON writes v to path atomically, through a temporary file.
func (st Store) writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, append(data, '\n'))
}
