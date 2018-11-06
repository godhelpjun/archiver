package archive

import (
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/nwaples/rardecode"
)

// Rar provides facilities for reading RAR archives.
// See https://www.rarlab.com/technote.htm.
type Rar struct {
	// Whether to overwrite existing files; if false,
	// an error is returned if the file exists.
	OverwriteExisting bool

	// Whether to make all the directories necessary
	// to create a rar archive in the desired path.
	MkdirAll bool

	// A single top-level folder can be implicitly
	// created by the Unarchive method if the files
	// to be extracted from the archive do not all
	// have a common root. This roughly mimics the
	// behavior of archival tools integrated into OS
	// file browsers which create a subfolder to
	// avoid unexpectedly littering the destination
	// folder with potentially many files, causing a
	// problematic cleanup/organization situation.
	// This feature is available for both creation
	// and extraction of archives, but may be slightly
	// inefficient with lots and lots of files,
	// especially on extraction.
	ImplicitTopLevelFolder bool

	// If true, errors encountered during reading
	// or writing a single file will be logged and
	// the operation will continue on remaining files.
	ContinueOnError bool

	// The password to open archives (optional).
	Password string

	rr *rardecode.Reader
}

// Unarchive unpacks the .rar file at source to destination.
// Destination will be treated as a folder name.
func (r *Rar) Unarchive(source, destination string) error {
	if !fileExists(destination) && r.MkdirAll {
		err := mkdir(destination)
		if err != nil {
			return fmt.Errorf("preparing destination: %v", err)
		}
	}

	// if the files in the archive do not all share a common
	// root, then make sure we extract to a single subfolder
	// rather than potentially littering the destination...
	if r.ImplicitTopLevelFolder {
		var err error
		destination, err = r.addTopLevelFolder(source, destination)
		if err != nil {
			return fmt.Errorf("scanning source archive: %v", err)
		}
	}

	file, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("opening source archive: %v", err)
	}
	defer file.Close()

	err = r.Open(file, 0)
	if err != nil {
		return fmt.Errorf("opening rar archive for reading: %v", err)
	}
	defer r.Close()

	for {
		err := r.unrarNext(destination)
		if err == io.EOF {
			break
		}
		if err != nil {
			if r.ContinueOnError {
				log.Printf("[ERROR] Reading file in rar archive: %v", err)
				continue
			}
			return fmt.Errorf("reading file in rar archive: %v", err)
		}
	}

	return nil
}

// addTopLevelFolder scans the files contained inside
// the tarball named sourceArchive and returns a modified
// destination if all the files do not share the same
// top-level folder.
func (r *Rar) addTopLevelFolder(sourceArchive, destination string) (string, error) {
	file, err := os.Open(sourceArchive)
	if err != nil {
		return "", fmt.Errorf("opening source archive: %v", err)
	}
	defer file.Close()

	rr, err := rardecode.NewReader(file, r.Password)
	if err != nil {
		return "", fmt.Errorf("creating archive reader: %v", err)
	}

	var files []string
	for {
		hdr, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("scanning tarball's file listing: %v", err)
		}
		files = append(files, hdr.Name)
	}

	if multipleTopLevels(files) {
		destination = filepath.Join(destination, folderNameFromFileName(sourceArchive))
	}

	return destination, nil
}

func (r *Rar) unrarNext(to string) error {
	f, err := r.Read()
	if err != nil {
		return err // don't wrap error; calling loop must break on io.EOF
	}
	header, ok := f.Header.(*rardecode.FileHeader)
	if !ok {
		return fmt.Errorf("expected header to be *rardecode.FileHeader but was %T", f.Header)
	}
	return r.unrarFile(f, filepath.Join(to, header.Name))
}

func (r *Rar) unrarFile(f File, to string) error {
	// do not overwrite existing files, if configured
	if !f.IsDir() && !r.OverwriteExisting && fileExists(to) {
		return fmt.Errorf("file already exists: %s", to)
	}

	hdr, ok := f.Header.(*rardecode.FileHeader)
	if !ok {
		return fmt.Errorf("expected header to be *rardecode.FileHeader but was %T", f.Header)
	}

	// if files come before their containing folders, then we must
	// create their folders before writing the file
	err := mkdir(filepath.Dir(to))
	if err != nil {
		return fmt.Errorf("making parent directories: %v", err)
	}

	return writeNewFile(to, r.rr, hdr.Mode())
}

// Open opens t for reading an archive from
// in. The size parameter is not used.
func (r *Rar) Open(in io.Reader, size int64) error {
	if r.rr != nil {
		return fmt.Errorf("rar archive is already open for reading")
	}
	var err error
	r.rr, err = rardecode.NewReader(in, r.Password)
	return err
}

// Read reads the next file from t, which must have
// already been opened for reading. If there are no
// more files, the error is io.EOF. The File must
// be closed when finished reading from it.
func (r *Rar) Read() (File, error) {
	if r.rr == nil {
		return File{}, fmt.Errorf("rar archive is not open")
	}

	hdr, err := r.rr.Next()
	if err != nil {
		return File{}, err // don't wrap error; preserve io.EOF
	}

	file := File{
		FileInfo:   rarFileInfo{hdr},
		Header:     hdr,
		ReadCloser: ReadFakeCloser{r.rr},
	}

	return file, nil
}

// Close closes the rar archive(s) opened by Create and Open.
func (r *Rar) Close() error {
	return nil
}

// Walk calls walkFn for each visited item in archive.
func (r *Rar) Walk(archive string, walkFn WalkFunc) error {
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("opening archive file: %v", err)
	}
	defer file.Close()

	err = r.Open(file, 0)
	if err != nil {
		return fmt.Errorf("opening archive: %v", err)
	}
	defer r.Close()

	for {
		f, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if r.ContinueOnError {
				log.Printf("[ERROR] Opening next file: %v", err)
				continue
			}
			return fmt.Errorf("opening next file: %v", err)
		}
		err = walkFn(f)
		if err != nil {
			if err == ErrStopWalk {
				break
			}
			if r.ContinueOnError {
				log.Printf("[ERROR] Walking %s: %v", f.Name(), err)
				continue
			}
			return fmt.Errorf("walking %s: %v", f.Name(), err)
		}
	}

	return nil
}

// Extract extracts a single file from the rar archive.
// If the target is a directory, the entire folder will
// be extracted into destination.
func (r *Rar) Extract(source, target, destination string) error {
	// target refers to a path inside the archive, which should be clean also
	target = path.Clean(target)

	// if the target ends up being a directory, then
	// we will continue walking and extracting files
	// until we are no longer within that directory
	var targetDirPath string

	return r.Walk(source, func(f File) error {
		th, ok := f.Header.(*rardecode.FileHeader)
		if !ok {
			return fmt.Errorf("expected header to be *rardecode.FileHeader but was %T", f.Header)
		}

		// importantly, cleaning the path strips tailing slash,
		// which must be appended to folders within the archive
		name := path.Clean(th.Name)
		if f.IsDir() && target == name {
			targetDirPath = path.Dir(name)
		}

		if within(target, th.Name) {
			// either this is the exact file we want, or is
			// in the directory we want to extract

			// build the filename we will extract to
			end, err := filepath.Rel(targetDirPath, th.Name)
			if err != nil {
				return fmt.Errorf("relativizing paths: %v", err)
			}
			joined := filepath.Join(destination, end)

			err = r.unrarFile(f, joined)
			if err != nil {
				return fmt.Errorf("extracting file %s: %v", th.Name, err)
			}

			// if our target was not a directory, stop walk
			if targetDirPath == "" {
				return ErrStopWalk
			}
		} else if targetDirPath != "" {
			// finished walking the entire directory
			return ErrStopWalk
		}

		return nil
	})
}

type rarFileInfo struct {
	fh *rardecode.FileHeader
}

func (rfi rarFileInfo) Name() string       { return rfi.fh.Name }
func (rfi rarFileInfo) Size() int64        { return rfi.fh.UnPackedSize }
func (rfi rarFileInfo) Mode() os.FileMode  { return rfi.fh.Mode() }
func (rfi rarFileInfo) ModTime() time.Time { return rfi.fh.ModificationTime }
func (rfi rarFileInfo) IsDir() bool        { return rfi.fh.IsDir }
func (rfi rarFileInfo) Sys() interface{}   { return nil }

// Compile-time checks to ensure type implements desired interfaces.
var (
	_ = Reader(new(Rar))
	_ = Unarchiver(new(Rar))
	_ = Walker(new(Rar))
	_ = Extractor(new(Rar))
	_ = os.FileInfo(rarFileInfo{})
)

// DefaultRar is a convenient archiver ready to use.
var DefaultRar = &Rar{
	MkdirAll: true,
}