package proto

import "time"

const FileDownloadChunkBytes = 1 << 20

type FileChunkRequest struct {
	Path     string `json:"path"`
	Cwd      string `json:"cwd"`
	Offset   int64  `json:"offset"`
	Length   int    `json:"length"`
	Revision string `json:"revision,omitempty"`
}

type FileChunk struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Mime     string    `json:"mime"`
	Modified time.Time `json:"modified"`
	Revision string    `json:"revision"`
	Offset   int64     `json:"offset"`
	Data     []byte    `json:"data,omitempty"`
}

// BrowserDownloadInfo is a named file in the browser machine's Downloads directory and the hash of its bytes.
type BrowserDownloadInfo struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// BrowserDownloadChunk carries part of a browser download and its byte range.
type BrowserDownloadChunk struct {
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	Data   []byte `json:"data"`
	EOF    bool   `json:"eof"`
}

// Thumbnails (GET /api/v1/machines/{mid}/file/thumb → node file.thumb): an image of a file at most Size pixels on
// its long side, for file lists and preview cards.
const (
	ThumbDefault = 256
	ThumbMin     = 32
	ThumbMax     = 1024
	// CodeNoThumbnail: the machine cannot draw this file (no decoder, no system tool); the client shows an icon (404).
	CodeNoThumbnail = "no_thumbnail"
)

type FileThumbRequest struct {
	Path string `json:"path"`
	Cwd  string `json:"cwd"`
	Size int    `json:"size"`
	// ETag the client already has (If-None-Match): the same version at the same size answers NotModified, no data.
	ETag string `json:"etag,omitempty"`
}

type FileThumb struct {
	Mime        string `json:"mime"`
	ETag        string `json:"etag"`
	NotModified bool   `json:"not_modified,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

// Converting a document for the web's viewer (GET /api/v1/machines/{mid}/file/convert → node file.convert): the
// machine's LibreOffice writes a PDF into the node's cache, which the client then reads like any file.
const CodeNoConverter = "no_converter" // the machine has no LibreOffice (404)

type FileConvertRequest struct {
	Path string `json:"path"`
	Cwd  string `json:"cwd"`
	To   string `json:"to"` // "pdf"
}

type FileConverted struct {
	Path string `json:"path"` // absolute, on the machine
	Size int64  `json:"size"`
	Mime string `json:"mime"`
}

// A file at one version of its repository (GET /api/v1/machines/{mid}/git/blob → node git.blob), written into the
// node's cache for comparing: Rev is HEAD, INDEX (staged), a commit hash, or a hash followed by ^ (its parent); Path is
// relative to the repository root.
type GitBlobRequest struct {
	Cwd  string `json:"cwd"`
	Path string `json:"path"`
	Rev  string `json:"rev"`
}

type GitBlob struct {
	Path    string `json:"path,omitempty"` // on the machine; read with file/download
	Size    int64  `json:"size,omitempty"`
	Missing bool   `json:"missing,omitempty"` // the file does not exist at that version
}
