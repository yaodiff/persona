package assistantwire

// boardRoutes are the workspace board's routes (docs/web/workspace-board.md): the manager keeps the board and its switch
// (workspace_board.go there) and finds the computers' workspaces; the hub relays them like Routes. The events the
// apps get of it (workspace_board, workspace_board_settings) come as ws.broadcast.
var boardRoutes = []string{
	"GET /api/v1/workspace-board",
	"POST /api/v1/workspace-board",
	"GET /api/v1/workspace-board/activity",
	"GET /api/v1/workspace-board/activity/{revision}",
	"GET /api/v1/workspaces",
	"GET /api/v1/workspace-board/settings",
	"PUT /api/v1/workspace-board/settings",
}

func init() { Routes = append(Routes, boardRoutes...) }
