package hub

import (
	"net/http"
	"strings"
)

// fileRoutes are a machine's files and folders, read and uploaded through its node.
func (h *Hub) fileRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/dirs", h.proxy("dirs.list", func(r *http.Request) any {
		return map[string]any{"path": r.URL.Query().Get("path")}
	}))
	api.HandleFunc("GET /api/v1/machines/{mid}/file/download", h.withMachine(h.downloadFile))
	api.HandleFunc("GET /api/v1/machines/{mid}/file/thumb", h.withMachine(h.fileThumb))
	api.HandleFunc("GET /api/v1/machines/{mid}/file/convert", h.withMachine(h.fileConvert))
	// stream=1: a binary file comes back described (stream, revision), its bytes from file/download.
	api.HandleFunc("GET /api/v1/machines/{mid}/file", h.proxy("file.read", func(r *http.Request) any {
		q := r.URL.Query()
		return map[string]any{"path": q.Get("path"), "cwd": q.Get("cwd"), "offset": atoi(q.Get("offset")), "stream": q.Get("stream") == "1"}
	}))
	// Which of the paths a conversation mentions are there on the machine (?paths=a%0Ab or ?path=a&path=b, and cwd):
	// the phone links only those. detail=1: the file page's Info sheet (hub feature file_info).
	api.HandleFunc("GET /api/v1/machines/{mid}/stat", h.proxy("fs.stat", func(r *http.Request) any {
		q := r.URL.Query()
		paths := q["path"]
		if s := q.Get("paths"); s != "" {
			paths = append(paths, strings.Split(s, "\n")...)
		}
		return map[string]any{"paths": paths, "cwd": q.Get("cwd"), "detail": q.Get("detail") == "1"}
	}))
	api.HandleFunc("GET /api/v1/machines/{mid}/fs", h.proxy("fs.list", func(r *http.Request) any {
		return map[string]any{"path": r.URL.Query().Get("path"), "cwd": r.URL.Query().Get("cwd")}
	}))
	// #2438 files-find: every file under cwd (git-aware), for the web palette's file search. Read like /fs: full
	// credentials only (an answer-only device has no machines/{mid}/ route, answerReads does not match it).
	api.HandleFunc("GET /api/v1/machines/{mid}/fs/find", h.proxy("fs.find", func(r *http.Request) any {
		return map[string]any{"cwd": r.URL.Query().Get("cwd"), "limit": atoi(r.URL.Query().Get("limit"))}
	}))
	api.HandleFunc("POST /api/v1/machines/{mid}/files", h.withMachine(h.uploadFile))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/rename", h.fileEdit("rename"))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/move", h.fileEdit("move"))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/trash", h.fileEdit("trash"))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/restore", h.fileEdit("restore"))
	// #5828 / #5829: a new folder, a text file written (made, or replaced when it is still the version opened), a zip
	api.HandleFunc("POST /api/v1/machines/{mid}/files/mkdir", h.fileEdit("mkdir"))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/write", h.fileEdit("write"))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/zip", h.fileEdit("zip"))
	api.HandleFunc("POST /api/v1/machines/{mid}/files/copy", h.fileEdit("copy"))   // #5830: copy and duplicate
	api.HandleFunc("POST /api/v1/machines/{mid}/files/chmod", h.fileEdit("chmod")) // #5831: permissions
	// #6113: GET files/operations/{oid} is relayed through the device manager (devicewire.Routes).
}
