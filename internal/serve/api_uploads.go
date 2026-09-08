// api_uploads.go is the HTTP half of the archive upload surface (see
// uploads.go for the store and the rules): POST /api/v1/uploads accepts one
// multipart file part and stages it, DELETE /api/v1/uploads/{id} cancels an
// upload the user changed their mind about.
package serve

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// uploadFormField is the multipart field name the SPA sends the archive
// under. A part with any other name that still carries a filename is
// accepted too - the field name is a convention, not a gate - but a client
// following this one is guaranteed to work.
const uploadFormField = "file"

// uploadResponse is POST /api/v1/uploads' success document: the handle a
// plan request names the archive by, plus what was actually staged.
//
// It is serve's own type, not a core document, because nothing in core has
// a concept of "a file a browser sent us": the upload exists purely to turn
// a browser's file into the PATH `lmm import <archive>` has always taken.
type uploadResponse struct {
	// UploadID is the opaque handle. It is never a path, and no path is
	// ever derived from it by a client (uploads.go).
	UploadID uploadID `json:"upload_id"`
	// Filename is the basename that was staged - what the import parses the
	// mod's identity out of when no --source/--id is given.
	Filename string `json:"filename"`
	// Size is the number of bytes written, for a client that wants to
	// confirm the transfer.
	Size int64 `json:"size"`
}

// handleAPIUploadCreate answers POST /api/v1/uploads (multipart/form-data,
// exactly one file part) by streaming the part into a fresh staging
// directory under the service's data dir.
//
// Everything about this handler is about not trusting the client's half of
// the request: the body is capped before it is read, the part is copied
// straight to disk rather than buffered, the filename is reduced to a
// basename with an extension the extractor accepts, and the location on
// disk is chosen here.
func (s *Server) handleAPIUploadCreate(w http.ResponseWriter, r *http.Request) {
	// The cap goes on the raw body, so it bounds the WHOLE multipart
	// stream - a client cannot get past it with extra parts.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)

	reader, err := r.MultipartReader()
	if err != nil {
		s.writeAPIError(w, http.StatusUnsupportedMediaType,
			fmt.Errorf("this endpoint takes a multipart/form-data body with one file part: %w", err))
		return
	}

	part, name, err := nextFilePart(reader)
	if err != nil {
		s.writeAPIError(w, uploadRequestStatus(err), err)
		return
	}
	defer part.Close() //nolint:errcheck // the copy below is what reports a read failure

	dir, err := s.svc.NewStagingDir("upload-*")
	if err != nil {
		s.writeAPIError(w, http.StatusInternalServerError, err)
		return
	}

	size, err := stageUploadedFile(filepath.Join(dir, name), part)
	if err == nil {
		// Only now that the body has been consumed can the rest of it be
		// checked: a second archive means the client misunderstood the
		// endpoint, and importing one of the two files for it would import
		// a file the user did not choose.
		err = refuseFurtherFileParts(reader)
	}
	if err != nil {
		_ = os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a directory nothing references
		s.writeAPIError(w, uploadRequestStatus(err), err)
		return
	}

	staged := &stagedUpload{Filename: name, Size: size, Dir: dir, Path: filepath.Join(dir, name)}
	id := s.uploads.Put(staged)
	s.log.Debug("serve: staged an upload", "id", id, "filename", name, "size", size)
	s.writeJSON(w, http.StatusOK, uploadResponse{UploadID: id, Filename: name, Size: size})
}

// handleAPIUploadDelete answers DELETE /api/v1/uploads/{id} by removing the
// staged archive and its directory. 204 rather than a document: there is
// nothing left to describe, and the client already knows which handle it
// cancelled.
func (s *Server) handleAPIUploadDelete(w http.ResponseWriter, r *http.Request) {
	id := uploadID(r.PathValue("id"))
	if !s.uploads.Remove(id) {
		s.writeAPIError(w, http.StatusNotFound, fmt.Errorf("unknown upload %q", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// errNoFilePart and errTooManyFileParts are the two shapes of "the
// multipart body was not what this endpoint takes".
var (
	errNoFilePart       = errors.New("the request carried no file part")
	errTooManyFileParts = errors.New("this endpoint takes exactly one file")
)

// refuseFurtherFileParts drains the rest of the multipart body and reports
// errTooManyFileParts if another file part turns up. Plain form fields are
// ignored, as they are on the way in.
func refuseFurtherFileParts(reader *multipart.Reader) error {
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading the upload: %w", err)
		}
		filename := part.FileName()
		_ = part.Close() //nolint:errcheck // nothing was read from it
		if filename != "" {
			return errTooManyFileParts
		}
	}
}

// nextFilePart advances reader to its first part carrying a filename and
// returns it alongside the sanitised basename to stage it under. A second
// file part is refused rather than silently ignored - a client sending two
// archives has misunderstood the endpoint, and picking one for it would
// import a file the user did not choose.
func nextFilePart(reader *multipart.Reader) (*multipart.Part, string, error) {
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, "", errNoFilePart
		}
		if err != nil {
			return nil, "", fmt.Errorf("reading the upload: %w", err)
		}
		if part.FileName() == "" {
			// A plain form field (the SPA sends none today); skip it.
			_ = part.Close() //nolint:errcheck // nothing was read from it
			continue
		}
		name, err := safeUploadName(part.FileName())
		if err != nil {
			_ = part.Close() //nolint:errcheck // the name is what failed, not the body
			return nil, "", err
		}
		return part, name, nil
	}
}

// safeUploadName reduces a client-supplied filename to a basename this
// server is willing to create, or refuses it.
//
// Two rules, both narrow on purpose. The name is reduced to its BASENAME -
// on BOTH separators, since a browser on Windows sends a backslash path and
// this server runs on Linux, where filepath.Base would keep the whole
// string - so nothing a client sends can escape the staging directory or
// name a dotfile. And its extension must be one the extractor actually
// handles, asked of core.Extractor itself rather than restated here, so the
// allow-list cannot drift from what the import will do with the file.
//
// A path is reduced rather than refused: the file a browser names as
// /home/someone/Downloads/Mod.zip or C:\Users\me\Mod.zip is a perfectly
// good archive, and only its last component was ever going to be used.
func safeUploadName(filename string) (string, error) {
	name := filename
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	switch {
	case name == "", name == ".", name == "..":
		return "", fmt.Errorf("%q is not a usable file name", filename)
	case strings.HasPrefix(name, "."):
		return "", fmt.Errorf("%q is not a usable file name", filename)
	}
	if core.NewExtractor().DetectFormat(name) == "" {
		return "", fmt.Errorf("%s is not an archive lmm can extract (accepted: .zip, .7z, .rar)", name)
	}
	return name, nil
}

// stageUploadedFile copies src to path, creating it 0600, and returns how
// many bytes were written. The copy is streamed: nothing here holds the
// archive in memory.
func stageUploadedFile(path string, src io.Reader) (int64, error) {
	// 0600 to match the 0700 staging directory: an in-flight upload is the
	// user's own content and has no reason to be world-readable.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, fmt.Errorf("creating the staged file: %w", err)
	}
	size, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return 0, fmt.Errorf("staging the upload: %w", copyErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("closing the staged file: %w", closeErr)
	}
	return size, nil
}

// uploadRequestStatus classifies an upload failure: a body over
// maxUploadBytes is 413, everything else this function sees is the
// client's malformed request (400).
func uploadRequestStatus(err error) int {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
