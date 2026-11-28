package assistant

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Server-owned board projection tree for todos_v2 (#15774): computes status groups (with top-level
// goal folding and blocked/needs-you child surfacing), direction groups (preferring explicit
// direction_id), the By-to-do sidebar folded tree, and unified summary counts in one deterministic
// pass so clients stay thin renderers.
//
// All types here are intentionally unexported and all constants are untyped strings to respect
// internal/archguard domain layer baselines.

const (
	boardSectionNeedsYou      = assistantwire.TodoListSectionNeedsYou
	boardSectionCanWait       = assistantwire.TodoListSectionCanWait
	boardSectionInProgress    = "in_progress"
	boardSectionPendingReview = "pending_review"
	boardSectionWaiting       = assistantwire.TodoListSectionWaiting
	boardSectionTodo          = assistantwire.TodoListSectionTodo
	boardSectionSomeday       = assistantwire.TodoListSectionSomeday
	boardSectionDone          = assistantwire.TodoListSectionDone
	boardSectionCancelled     = "cancelled"
)

var boardSectionOrder = [...]string{
	boardSectionNeedsYou,
	boardSectionCanWait,
	boardSectionInProgress,
	boardSectionPendingReview,
	boardSectionWaiting,
	boardSectionTodo,
	boardSectionSomeday,
	boardSectionDone,
	boardSectionCancelled,
}

type todoBoardProjection struct {
	StatusGroups    []todoBoardStatusGroup    `json:"status_groups"`
	DirectionGroups []todoBoardDirectionGroup `json:"direction_groups"`
	SidebarTree     []todoBoardSidebarNode    `json:"sidebar_tree"`
	Counts          todoBoardCounts           `json:"counts"`
}

type todoBoardStatusGroup struct {
	Key          string           `json:"key"`
	TotalCount   int              `json:"total_count"`
	VisibleCount int              `json:"visible_count"`
	Entries      []todoBoardEntry `json:"entries"`
}

type todoBoardEntry struct {
	TodoID           string   `json:"todo_id"`
	FoldedChildIDs   []string `json:"folded_child_ids,omitempty"`
	SurfacedChildIDs []string `json:"surfaced_child_ids,omitempty"`
	FoldedCount      int      `json:"folded_count,omitempty"`
}

type todoBoardDirectionGroup struct {
	DirectionID string                   `json:"direction_id"`
	Label       string                   `json:"label,omitempty"`
	Counts      todoBoardDirectionCounts `json:"counts"`
	TodoIDs     []string                 `json:"todo_ids"`
}

type todoBoardDirectionCounts struct {
	NeedsYou      int `json:"needs_you"`
	InProgress    int `json:"in_progress"`
	PendingReview int `json:"pending_review"`
	Waiting       int `json:"waiting"`
	Done          int `json:"done"`
	Total         int `json:"total"`
}

type todoBoardSidebarNode struct {
	TodoID           string   `json:"todo_id"`
	Section          string   `json:"section"`
	ItemType         string   `json:"item_type"`
	DirectionID      string   `json:"direction_id,omitempty"`
	ChildIDs         []string `json:"child_ids,omitempty"`
	SurfacedChildIDs []string `json:"surfaced_child_ids,omitempty"`
	FoldedCount      int      `json:"folded_count"`
	NeedsYouCount    int      `json:"needs_you_count"`
	ActiveCount      int      `json:"active_count"`
}

type todoBoardCounts struct {
	NeedsYou      int `json:"needs_you"`
	CanWait       int `json:"can_wait"`
	InProgress    int `json:"in_progress"`
	PendingReview int `json:"pending_review"`
	Waiting       int `json:"waiting"`
	Todo          int `json:"todo"`
	Someday       int `json:"someday"`
	Done          int `json:"done"`
	Cancelled     int `json:"cancelled"`
	OpenTotal     int `json:"open_total"`
}

type todoBoardItemMeta struct {
	item        listedTodo
	section     string
	subOrder    int
	viewRank    int
	rootGoalID  string
	surfaced    bool
	needsYou    bool
	carried     bool
	directionID string
	itemType    string
}

// buildTodoBoardProjectionWithOptions deterministically computes the complete server-owned board projection tree
// for GET /api/v1/todos under todos_v2=true from the projected rows and their list counts.
func buildTodoBoardProjectionWithOptions(items []listedTodo, directions map[string]string, foldChildren bool, listCounts assistantwire.TodoCounts) *todoBoardProjection {
	metaByID := make(map[string]*todoBoardItemMeta, len(items))
	orderedIDs := make([]string, 0, len(items))

	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if _, dup := metaByID[item.ID]; dup {
			continue
		}
		sec, sub := classifyBoardSection(item)
		rank := math.MaxInt
		needsYou := sec == boardSectionNeedsYou && !item.Quiet
		if item.View != nil {
			rank = item.View.Rank
			needsYou = projectionViewNeedsYou(*item.View, item.Quiet)
		}
		itemType := strings.TrimSpace(item.ItemType)
		if itemType == "" {
			itemType = effectiveTodoItemType(item.Todo, false)
		}
		metaByID[item.ID] = &todoBoardItemMeta{
			item:        item,
			section:     sec,
			subOrder:    sub,
			viewRank:    rank,
			directionID: item.EffectiveDirectionID,
			itemType:    itemType,
			needsYou:    needsYou,
		}
		orderedIDs = append(orderedIDs, item.ID)
	}

	// Board section totals for display include folded rows. Summary counts follow the list's direct-parent rollup,
	// independently of whether the user folds children on the board.
	countedBySection := make(map[string]int, len(boardSectionOrder))
	for _, id := range orderedIDs {
		m := metaByID[id]
		parent := metaByID[m.item.PartOf]
		m.carried = todoCarriedByParent(m.item.Todo, parent != nil && !isBoardClosedSection(parent.section))
		if !m.carried {
			countedBySection[m.section]++
		}
	}

	// Sort all items deterministically before resolving folding so output never depends on input order.
	slices.SortFunc(orderedIDs, func(a, b string) int {
		return compareBoardMeta(metaByID[a], metaByID[b])
	})

	// Resolve cycle-safe top-level ancestor goal and whether each child surfaces as a blocker.
	foldedChildren := make(map[string][]string)
	surfacedChildren := make(map[string][]string)
	if foldChildren {
		for _, id := range orderedIDs {
			m := metaByID[id]
			rootID := resolveBoardRootGoalID(id, metaByID)
			if rootID == "" {
				continue
			}
			// Never fold an open child under a closed parent; open work must stay visible.
			if isBoardClosedSection(metaByID[rootID].section) && !isBoardClosedSection(m.section) {
				continue
			}
			m.rootGoalID = rootID
			if m.section == boardSectionNeedsYou {
				m.surfaced = true
				surfacedChildren[rootID] = append(surfacedChildren[rootID], id)
			} else {
				foldedChildren[rootID] = append(foldedChildren[rootID], id)
			}
		}
	}

	// 1. Build status_groups.
	rawBySection := make(map[string]int, len(boardSectionOrder))
	entriesBySection := make(map[string][]todoBoardEntry, len(boardSectionOrder))
	for _, key := range boardSectionOrder {
		entriesBySection[key] = []todoBoardEntry{}
	}

	for _, id := range orderedIDs {
		m := metaByID[id]
		rawBySection[m.section]++
		// Folded non-surfaced children live inside their top-level parent entry.
		if m.rootGoalID != "" && !m.surfaced {
			continue
		}
		entry := todoBoardEntry{
			TodoID: id,
		}
		if fc := foldedChildren[id]; len(fc) > 0 {
			entry.FoldedChildIDs = fc
			entry.FoldedCount = len(fc)
		}
		if sc := surfacedChildren[id]; len(sc) > 0 {
			entry.SurfacedChildIDs = sc
		}
		entriesBySection[m.section] = append(entriesBySection[m.section], entry)
	}

	statusGroups := make([]todoBoardStatusGroup, 0, len(boardSectionOrder))
	for _, key := range boardSectionOrder {
		entries := entriesBySection[key]
		statusGroups = append(statusGroups, todoBoardStatusGroup{
			Key:          key,
			TotalCount:   rawBySection[key],
			VisibleCount: len(entries),
			Entries:      entries,
		})
	}

	counts := todoBoardCounts{
		NeedsYou:      listCounts.NeedsYou,
		CanWait:       countedBySection[boardSectionCanWait],
		InProgress:    countedBySection[boardSectionInProgress],
		PendingReview: countedBySection[boardSectionPendingReview],
		Waiting:       countedBySection[boardSectionWaiting],
		Todo:          countedBySection[boardSectionTodo],
		Someday:       countedBySection[boardSectionSomeday],
		Done:          countedBySection[boardSectionDone],
		Cancelled:     countedBySection[boardSectionCancelled],
		OpenTotal:     listCounts.Open,
	}

	// 2. Build direction_groups.
	dirBuckets := make(map[string][]*todoBoardItemMeta)
	for _, id := range orderedIDs {
		m := metaByID[id]
		dirBuckets[m.directionID] = append(dirBuckets[m.directionID], m)
	}

	directionGroups := make([]todoBoardDirectionGroup, 0, len(dirBuckets))
	for dirID, bucket := range dirBuckets {
		slices.SortFunc(bucket, compareBoardDirectionMember)
		var dc todoBoardDirectionCounts
		todoIDs := make([]string, 0, len(bucket))
		for _, m := range bucket {
			todoIDs = append(todoIDs, m.item.ID)
			dc.Total++
			if m.needsYou && !m.carried {
				dc.NeedsYou++
			}
			switch m.section {
			case boardSectionInProgress:
				dc.InProgress++
			case boardSectionPendingReview:
				dc.PendingReview++
			case boardSectionWaiting:
				dc.Waiting++
			case boardSectionDone:
				dc.Done++
			}
		}
		label := ""
		if dirID != "" && directions != nil {
			label = strings.TrimSpace(directions[dirID])
		}
		directionGroups = append(directionGroups, todoBoardDirectionGroup{
			DirectionID: dirID,
			Label:       label,
			Counts:      dc,
			TodoIDs:     todoIDs,
		})
	}

	slices.SortFunc(directionGroups, compareBoardDirectionGroup)

	// 3. Build sidebar_tree (open top-level items + surfaced blockers, ordered by urgency).
	sidebarTree := make([]todoBoardSidebarNode, 0)
	for _, id := range orderedIDs {
		m := metaByID[id]
		if !isBoardOpenSection(m.section) {
			continue
		}
		if m.rootGoalID != "" && !m.surfaced {
			continue
		}
		fc := foldedChildren[id]
		sc := surfacedChildren[id]
		needsYouCount := 0
		activeCount := 0
		if m.needsYou {
			needsYouCount++
		}
		if m.section == boardSectionInProgress || m.section == boardSectionPendingReview {
			activeCount++
		}
		for _, cid := range fc {
			cm := metaByID[cid]
			if cm.needsYou && !(cm.carried && m.needsYou) {
				needsYouCount++
			}
			if cm.section == boardSectionInProgress || cm.section == boardSectionPendingReview {
				activeCount++
			}
		}
		for _, cid := range sc {
			cm := metaByID[cid]
			if cm.needsYou && !(cm.carried && m.needsYou) {
				needsYouCount++
			}
			if cm.section == boardSectionInProgress || cm.section == boardSectionPendingReview {
				activeCount++
			}
		}
		node := todoBoardSidebarNode{
			TodoID:        id,
			Section:       m.section,
			ItemType:      m.itemType,
			DirectionID:   m.directionID,
			FoldedCount:   len(fc),
			NeedsYouCount: needsYouCount,
			ActiveCount:   activeCount,
		}
		if len(fc) > 0 {
			node.ChildIDs = fc
		}
		if len(sc) > 0 {
			node.SurfacedChildIDs = sc
		}
		sidebarTree = append(sidebarTree, node)
	}

	return &todoBoardProjection{
		StatusGroups:    statusGroups,
		DirectionGroups: directionGroups,
		SidebarTree:     sidebarTree,
		Counts:          counts,
	}
}

func boardSectionPriority(section string) int {
	if i := slices.Index(boardSectionOrder[:], section); i >= 0 {
		return i
	}
	return len(boardSectionOrder)
}

func isBoardClosedSection(section string) bool {
	return section == boardSectionDone || section == boardSectionCancelled
}

func isBoardOpenSection(section string) bool {
	switch section {
	case boardSectionNeedsYou, boardSectionCanWait, boardSectionInProgress, boardSectionPendingReview, boardSectionWaiting, boardSectionTodo:
		return true
	default:
		return false
	}
}

// classifyBoardSection maps a listedTodo to a board section and a deterministic
// sub-section priority, respecting the server-computed todo_view when present.
func classifyBoardSection(t listedTodo) (section string, subOrder int) {
	if v := t.View; v != nil {
		switch {
		case v.Reason == assistantwire.TodoReasonCanceled || projectionViewIsClosed(*v) && t.Cancelled():
			return boardSectionCancelled, 0
		case v.Section == assistantwire.TodoListSectionCanWait:
			return boardSectionCanWait, 0
		case v.Section == assistantwire.TodoListSectionNeedsYou || v.Phase == assistantwire.TodoPhaseNeedsYou:
			return boardSectionNeedsYou, 0
		case projectionViewIsClosed(*v):
			if v.Section == assistantwire.TodoListSectionJustFinished {
				return boardSectionDone, 0
			}
			return boardSectionDone, 1
		case v.Section == assistantwire.TodoListSectionSomeday || v.Phase == assistantwire.TodoPhaseSomeday:
			return boardSectionSomeday, 0
		case t.Supervision != nil && t.Supervision.State == reviewPending:
			return boardSectionPendingReview, 0
		case v.Section == assistantwire.TodoListSectionJustFinished:
			return boardSectionPendingReview, 1
		case v.Section == assistantwire.TodoListSectionWorking || v.Phase == assistantwire.TodoPhaseWorking:
			return boardSectionInProgress, 0
		case v.Section == assistantwire.TodoListSectionWaiting || v.Phase == assistantwire.TodoPhaseWaiting:
			return boardSectionWaiting, 0
		default:
			return boardSectionTodo, 0
		}
	}

	// Fallback for direct unit-test inputs without precomputed View.
	switch {
	case t.Cancelled():
		return boardSectionCancelled, 0
	case t.closedForGood():
		return boardSectionDone, 1
	case t.SomedayUntil != nil:
		return boardSectionSomeday, 0
	case t.StartError != "" || t.Ask != nil || (t.Supervision != nil && t.Supervision.State == reviewNeedsUser):
		return boardSectionNeedsYou, 0
	case t.Supervision != nil && t.Supervision.State == reviewPending:
		return boardSectionPendingReview, 0
	case t.Supervision != nil && t.Supervision.State == reviewParked,
		t.Wait != nil,
		repoGateOpenNonDraftPR(t.RepoGate):
		return boardSectionWaiting, 0
	default:
		for _, a := range t.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil {
				return boardSectionInProgress, 0
			}
		}
		return boardSectionTodo, 0
	}
}

// resolveBoardRootGoalID walks part_of to the highest ancestor present in metaByID.
// Returns "" for top-level items, orphan children, self-references, or cycles.
func resolveBoardRootGoalID(id string, metaByID map[string]*todoBoardItemMeta) string {
	start := metaByID[id]
	if start == nil {
		return ""
	}
	parent := strings.TrimSpace(start.item.PartOf)
	if parent == "" || parent == id || metaByID[parent] == nil {
		return ""
	}
	visited := map[string]bool{id: true}
	cur := parent
	for cur != "" {
		if visited[cur] {
			return ""
		}
		visited[cur] = true
		node := metaByID[cur]
		if node == nil {
			return ""
		}
		next := strings.TrimSpace(node.item.PartOf)
		if next == "" || next == cur || metaByID[next] == nil {
			return cur
		}
		cur = next
	}
	return ""
}

// resolveDirectDirectionID resolves a to-do's own direction without traversing part_of links:
//  1. Explicit direction_id (trimmed; never overridden).
//  2. The supervisor's direction, including the owner of a task-leader Agent, when present in directions.
//  3. origin when present in directions.
func (tb *treeBuild) resolveDirectDirectionID(t listedTodo, directions map[string]string) string {
	if dirID := strings.TrimSpace(t.DirectionID); dirID != "" {
		return dirID
	}
	if len(directions) == 0 {
		return ""
	}
	if sup := strings.TrimSpace(tb.taskOwner(t.Todo)); sup != "" {
		if _, ok := directions[sup]; ok {
			return sup
		}
	}
	if origin := strings.TrimSpace(t.Origin); origin != "" {
		if _, ok := directions[origin]; ok {
			return origin
		}
	}
	return ""
}

// resolveEffectiveDirections computes the server-authoritative effective direction_id for every
// to-do in items with strict precedence and cycle-safe part_of inheritance:
//  1. Explicit direction_id (trimmed; never overridden).
//  2. The supervisor's direction (when present in directions).
//  3. origin (when present in directions).
//  4. part_of ancestor chain inheritance (nearest ancestor with a resolved direction).
//  5. Unanimous subtree child fallback for undirected umbrella parents: if all directed descendants
//     agree on one single direction in directions, inherit it for the parent and propagate to any
//     undirected siblings under that parent.
func (tb *treeBuild) resolveEffectiveDirections(items []listedTodo, directions map[string]string) map[string]string {
	byID := make(map[string]listedTodo, len(items))
	direct := make(map[string]string, len(items))
	orderedIDs := make([]string, 0, len(items))

	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if _, dup := byID[item.ID]; dup {
			continue
		}
		byID[item.ID] = item
		direct[item.ID] = tb.resolveDirectDirectionID(item, directions)
		orderedIDs = append(orderedIDs, item.ID)
	}

	children := make(map[string][]string)
	for _, id := range orderedIDs {
		parentID := strings.TrimSpace(byID[id].PartOf)
		if parentID == "" || parentID == id {
			continue
		}
		if _, ok := byID[parentID]; !ok {
			continue
		}
		children[parentID] = append(children[parentID], id)
	}

	// Pass 2: cycle-safe ancestor chain inheritance from direct directions.
	resolved := make(map[string]string, len(orderedIDs))
	for _, id := range orderedIDs {
		if d := direct[id]; d != "" {
			resolved[id] = d
			continue
		}
		visited := map[string]bool{id: true}
		cur := strings.TrimSpace(byID[id].PartOf)
		for cur != "" && !visited[cur] {
			visited[cur] = true
			parent, ok := byID[cur]
			if !ok {
				break
			}
			if d := direct[cur]; d != "" {
				resolved[id] = d
				break
			}
			cur = strings.TrimSpace(parent.PartOf)
		}
	}

	// Pass 3: unanimous subtree child fallback for undirected umbrella parents.
	if len(directions) > 0 {
		umbrella := make(map[string]string)
		for _, id := range orderedIDs {
			if resolved[id] != "" || len(children[id]) == 0 {
				continue
			}
			visited := map[string]bool{id: true}
			queue := append([]string(nil), children[id]...)
			sole := ""
			conflict := false
			for len(queue) > 0 && !conflict {
				descID := queue[0]
				queue = queue[1:]
				if visited[descID] {
					continue
				}
				visited[descID] = true
				if d := resolved[descID]; d != "" {
					if sole == "" {
						sole = d
					} else if sole != d {
						conflict = true
						break
					}
				}
				if kids := children[descID]; len(kids) > 0 {
					queue = append(queue, kids...)
				}
			}
			if !conflict && sole != "" {
				if _, known := directions[sole]; known {
					umbrella[id] = sole
				}
			}
		}
		if len(umbrella) > 0 {
			for id, dirID := range umbrella {
				resolved[id] = dirID
			}
			// Pass 4: propagate newly resolved umbrella parent directions to undirected descendants.
			for _, id := range orderedIDs {
				if resolved[id] != "" {
					continue
				}
				visited := map[string]bool{id: true}
				cur := strings.TrimSpace(byID[id].PartOf)
				for cur != "" && !visited[cur] {
					visited[cur] = true
					parent, ok := byID[cur]
					if !ok {
						break
					}
					if d := resolved[cur]; d != "" {
						resolved[id] = d
						break
					}
					cur = strings.TrimSpace(parent.PartOf)
				}
			}
		}
	}

	return resolved
}

// populateTodoEffectiveDirections attaches read-only effective_direction_id and
// effective_direction_name onto each listedTodo in items. Callers must invoke this only when
// todos_v2 is enabled.
func (s *Service) populateTodoEffectiveDirections(items []listedTodo, directions map[string]string) {
	if len(items) == 0 {
		return
	}
	resolved := s.directionOwners(items).resolveEffectiveDirections(items, directions)
	for i := range items {
		dirID := resolved[items[i].ID]
		items[i].EffectiveDirectionID = dirID
		items[i].EffectiveDirectionName = strings.TrimSpace(directions[dirID])
	}
}

func compareBoardMeta(a, b *todoBoardItemMeta) int {
	return cmp.Or(
		cmp.Compare(boardSectionPriority(a.section), boardSectionPriority(b.section)),
		cmp.Compare(a.subOrder, b.subOrder),
		cmp.Compare(a.viewRank, b.viewRank),
		b.item.CreatedAt.Compare(a.item.CreatedAt),
		strings.Compare(a.item.ID, b.item.ID),
	)
}

func compareBoardDirectionMember(a, b *todoBoardItemMeta) int {
	aIsChild := a.rootGoalID != ""
	bIsChild := b.rootGoalID != ""
	childOrder := 0
	if !aIsChild && bIsChild {
		childOrder = -1
	} else if aIsChild && !bIsChild {
		childOrder = 1
	}
	return cmp.Or(
		cmp.Compare(boardSectionPriority(a.section), boardSectionPriority(b.section)),
		childOrder,
		cmp.Compare(a.subOrder, b.subOrder),
		cmp.Compare(a.viewRank, b.viewRank),
		b.item.CreatedAt.Compare(a.item.CreatedAt),
		strings.Compare(a.item.ID, b.item.ID),
	)
}

func compareBoardDirectionGroup(a, b todoBoardDirectionGroup) int {
	// Ungrouped ("") is always sorted last.
	if a.DirectionID == "" && b.DirectionID != "" {
		return 1
	}
	if a.DirectionID != "" && b.DirectionID == "" {
		return -1
	}
	aHasNeeds := a.Counts.NeedsYou > 0
	bHasNeeds := b.Counts.NeedsYou > 0
	if aHasNeeds != bHasNeeds {
		if aHasNeeds {
			return -1
		}
		return 1
	}
	if aHasNeeds && a.Counts.NeedsYou != b.Counts.NeedsYou {
		return cmp.Compare(b.Counts.NeedsYou, a.Counts.NeedsYou)
	}
	aHasActive := a.Counts.InProgress > 0
	bHasActive := b.Counts.InProgress > 0
	if aHasActive != bHasActive {
		if aHasActive {
			return -1
		}
		return 1
	}
	if aHasActive && a.Counts.InProgress != b.Counts.InProgress {
		return cmp.Compare(b.Counts.InProgress, a.Counts.InProgress)
	}
	return cmp.Or(
		cmp.Compare(b.Counts.Total, a.Counts.Total),
		strings.Compare(a.DirectionID, b.DirectionID),
	)
}
