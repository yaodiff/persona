package orcart

import (
	"context"
	"fmt"
	"strings"
)

// The split directions terminal.split takes, in Orca's words for where the new pane goes: "vertical" puts it to the
// right (side by side), "horizontal" below (stacked), as Orca's own layout code places them.
const (
	SplitRight = "vertical"
	SplitDown  = "horizontal"
)

// Split is terminal.split of the terminal's pane: a new shell beside it, which runs command first when one is given.
// It returns the new terminal as terminal.list lists it (its incarnation included).
//
// On the local socket Orca 1.4.277 takes no cwd for this form (only the form naming a pane by its layout, which it
// refuses there: host_not_owned), so the shell starts in the worktree and a folder is reached by command. Neither this
// call nor CloseTab holds expectedIncarnationId there (checked live on an isolated runtime); it is sent all the same,
// for a runtime that does, and the caller looks the terminal up just before.
func (c *Client) Split(ctx context.Context, t Target, direction, command string) (*Terminal, error) {
	p := t.params()
	p["direction"] = direction
	if command != "" {
		p["command"] = command
	}
	var res struct {
		Split struct {
			Handle string `json:"handle"`
			TabID  string `json:"tabId"`
			LeafID string `json:"leafId"`
		} `json:"split"`
	}
	if err := c.Call(ctx, SendTimeout, "terminal.split", p, &res); err != nil {
		return nil, err
	}
	s := res.Split
	if s.Handle == "" {
		return nil, fmt.Errorf("orca terminal.split: no terminal handle in the answer; the split may have happened")
	}
	list, err := c.Terminals(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("orca terminal.split: split into %s but could not list it: %w", s.Handle, err)
	}
	for _, current := range list.Terminals {
		if current.Handle == s.Handle && current.IsLocal() && current.TabID != "" && current.LeafID != "" &&
			(s.LeafID == "" || current.LeafID == s.LeafID) {
			return &current, nil
		}
	}
	return nil, fmt.Errorf("orca terminal.split: split into %s, which terminal.list does not list", s.Handle)
}

// CloseTab is terminal.closeTab: Orca closes the tab the terminal is in, with every pane in it, whatever runs there.
func (c *Client) CloseTab(ctx context.Context, t Target) error {
	return c.Call(ctx, CloseTimeout, "terminal.closeTab", t.params(), nil)
}

// TabNames is session.tabs.list's view of a worktree's terminal tabs ("id:<worktreeId>"), the model Orca's phone app
// reads. shown has, for each tab, the name each of its panes shows now: its custom name when it has one (given by
// terminal.rename or a rename in Orca's window), else its title. custom has the tabs' custom names alone. Unlike the
// tab titles of terminal.list's layouts, it follows renames on a runtime with no window (Orca 1.4.277's headless
// server keeps a layout tab's title as created; checked live).
func (c *Client) TabNames(ctx context.Context, worktree string) (shown map[string][]string, custom map[string]string, err error) {
	var res struct {
		Tabs []struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			ParentTabID string `json:"parentTabId"`
			Title       string `json:"title"`
			CustomTitle string `json:"customTitle"`
		} `json:"tabs"`
	}
	if err := c.Call(ctx, CallTimeout, "session.tabs.list", map[string]any{"worktree": worktree}, &res); err != nil {
		return nil, nil, err
	}
	shown, custom = map[string][]string{}, map[string]string{}
	for _, t := range res.Tabs {
		if t.Type != "terminal" {
			continue
		}
		tab := t.ParentTabID
		if tab == "" {
			tab, _, _ = strings.Cut(t.ID, "::")
		}
		name := strings.TrimSpace(t.CustomTitle)
		if name != "" {
			custom[tab] = name
		} else {
			name = strings.TrimSpace(t.Title)
		}
		shown[tab] = append(shown[tab], name)
	}
	return shown, custom, nil
}

// RuntimeID is the id of the runtime whose metadata the client read last ("" before any call). It changes when Orca
// starts again (an update included), so what is known of one runtime can be dropped for the next.
func (c *Client) RuntimeID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.meta == nil {
		return ""
	}
	return c.meta.runtimeID
}
