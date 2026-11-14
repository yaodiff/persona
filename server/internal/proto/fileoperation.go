package proto

// File operation states reported while a node's copy or zip is running and briefly after it settles.
const (
	FileOperationRunning   = "running"
	FileOperationDone      = "done"
	FileOperationFailed    = "failed"
	FileOperationCancelled = "cancelled"
)

// FileOperation is the pollable progress of one file.copy or file.zip call. Byte counts are source bytes before
// compression; file counts include the directories and links accepted into the operation, not only regular files.
type FileOperation struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	FilesDone  int    `json:"files_done"`
	FilesTotal int    `json:"files_total"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
}
