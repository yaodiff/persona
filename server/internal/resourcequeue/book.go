package resourcequeue

import (
	"errors"
	"slices"
	"sort"
	"time"
)

// Advance preserves the external runner's hard locks: expired clients never imply a remote job was killed.
func Advance(rows []Lease, capacity, external int, now time.Time) {
	held := external
	var token int64
	for _, l := range rows {
		if l.Token > token {
			token = l.Token
		}
	}
	Expire(rows, now)
	for i := range rows {
		l := &rows[i]
		if l.State == "granted" {
			held++
		}
	}
	order := make([]int, 0)
	for i := range rows {
		if rows[i].State == "queued" && !rows[i].External {
			order = append(order, i)
		}
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := rows[order[i]], rows[order[j]]
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.Seq < b.Seq
	})
	for _, i := range order {
		if held >= capacity {
			break
		}
		l := &rows[i]
		l.State = "granted"
		token++
		l.Token = token
		l.GrantedAt = now
		l.HeartbeatUntil = now.Add(90 * time.Second)
		l.HoldUntil = now.Add(time.Hour)
		held++
	}
}
func Acquire(rows *[]Lease, in Request, now time.Time, id string, seq int64) (Reply, error) {
	key, err := Key(in.Project, in.Commit, in.Groups)
	if err != nil {
		return Reply{}, err
	}
	if in.Holder.Machine == "" || in.Holder.Session == "" {
		return Reply{}, errors.New("a verified session is required")
	}
	for i := range *rows {
		l := &(*rows)[i]
		if in.ID != "" && l.ID == in.ID && l.Key != key {
			return Reply{}, errors.New("request ID already belongs to another key")
		}
		if l.Key != key {
			continue
		}
		if l.State == "queued" || l.State == "granted" {
			if in.ID == l.ID && l.Holder.Owner() == in.Holder.Owner() {
				return Reply{Status: l.State, Lease: l}, nil
			}
			if !slices.Contains(l.Joiners, in.Holder.Owner()) && l.Holder.Owner() != in.Holder.Owner() {
				l.Joiners = append(l.Joiners, in.Holder.Owner())
			}
			return Reply{Status: "joined", Lease: l}, nil
		}
	}
	// A current run takes precedence over retained successful history for the same key.
	for i := len(*rows) - 1; i >= 0; i-- {
		l := &(*rows)[i]
		if l.Key == key && l.State == "released" && CacheOK(l.Outcome, now) {
			return Reply{Status: "cached", Lease: l}, nil
		}
	}
	if in.ID != "" {
		id = in.ID
	}
	for _, l := range *rows {
		if l.ID == id {
			return Reply{}, errors.New("request ID already used")
		}
	}
	n := 0
	pending := []string{}
	for _, l := range *rows {
		if l.State == "queued" && l.Holder.Owner() == in.Holder.Owner() {
			n++
			pending = append(pending, l.ID)
		}
	}
	if n >= 2 {
		return Reply{Status: "refused", Pending: pending}, nil
	}
	l := Lease{Priority: in.Priority, ID: id, Seq: seq, Resource: in.Resource, Key: key, State: "queued", Holder: in.Holder, CreatedAt: now, HeartbeatUntil: now.Add(90 * time.Second), WaitUntil: now.Add(90 * time.Minute)}
	if CacheOK(in.Cached, now) {
		l.State = "released"
		l.Outcome = in.Cached
	}
	*rows = append(*rows, l)
	status := "queued"
	if l.State == "released" {
		status = "cached"
	}
	return Reply{Status: status, Lease: &(*rows)[len(*rows)-1]}, nil
}
func CacheOK(o *Outcome, now time.Time) bool {
	return o != nil && o.Exit == 0 && o.Run != "" && !o.FinishedAt.IsZero() && !o.FinishedAt.After(now) && now.Sub(o.FinishedAt) <= 12*time.Hour
}
func Mutate(rows []Lease, in Request, now time.Time) (Reply, error) {
	for i := range rows {
		l := &rows[i]
		if l.ID != in.ID {
			continue
		}
		owner := l.Holder.Owner() == in.Holder.Owner()
		if !owner && !slices.Contains(l.Joiners, in.Holder.Owner()) {
			return Reply{}, errors.New("lease not found")
		}
		if in.Action == "get" {
			return Reply{Status: l.State, Lease: l}, nil
		}
		if !owner {
			return Reply{}, errors.New("only the holder may change a lease")
		}
		if in.Action == "cancel" && l.State == "queued" {
			if (!l.HeartbeatUntil.IsZero() && !now.Before(l.HeartbeatUntil)) || (!l.WaitUntil.IsZero() && !now.Before(l.WaitUntil)) {
				return Reply{Status: "fenced"}, nil
			}
			l.State = "cancelled"
			return Reply{Status: l.State, Lease: l}, nil
		}
		if in.Action == "renew" && l.State == "queued" && in.Token == 0 && now.Before(l.HeartbeatUntil) && (l.WaitUntil.IsZero() || now.Before(l.WaitUntil)) {
			l.HeartbeatUntil = now.Add(90 * time.Second)
			return Reply{Status: l.State, Lease: l}, nil
		}
		if l.State != "granted" || l.Token != in.Token || !now.Before(l.HeartbeatUntil) || (!l.HoldUntil.IsZero() && !now.Before(l.HoldUntil)) {
			return Reply{Status: "fenced"}, nil
		}
		switch in.Action {
		case "renew":
			l.HeartbeatUntil = now.Add(90 * time.Second)
		case "release":
			l.State = "released"
			l.Outcome = in.Outcome
			if l.Outcome != nil {
				l.Outcome.FinishedAt = now
				l.ExternalRef = l.Outcome.Run
			}
		default:
			return Reply{}, errors.New("unknown resource action")
		}
		return Reply{Status: l.State, Lease: l}, nil
	}
	return Reply{}, errors.New("lease not found")
}

func Expire(rows []Lease, now time.Time) {
	for i := range rows {
		l := &rows[i]
		if l.State == "granted" && (!now.Before(l.HeartbeatUntil) || !now.Before(l.HoldUntil)) {
			l.State = "expired"
		}
		if l.State == "queued" && !l.HeartbeatUntil.IsZero() && !now.Before(l.HeartbeatUntil) {
			l.State = "expired"
		}
		if l.State == "queued" && !now.Before(l.WaitUntil) {
			l.State = "timeout"
		}
	}
}
