//go:build ee

// SPDX-License-Identifier: LicenseRef-KaaS-EE

package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/KaaS-team/factory/ee/contract"
	"github.com/KaaS-team/ui"
)

// WorkerInfo is one registered worker as the registry reports it.
type WorkerInfo struct {
	ID       string
	Pool     string
	Labels   map[string]string
	Caps     []string
	Draining bool
	LastSeen time.Time
	Job      string // the job it holds, empty when idle
}

// Workers is the worker registry the console reads and drains. TTL is how long a worker lives
// without a heartbeat (broker.Leaser.TTL).
type Workers interface {
	List() []WorkerInfo
	Drain(id string) error
	TTL() time.Duration
}

// Health is what the console says about a worker.
type Health string

// The healths. A worker is late after one missed TTL and lost after three.
const (
	Healthy  Health = "healthy"
	Late     Health = "late"
	Lost     Health = "lost"
	Draining Health = "draining"
)

// HealthOf grades w at now against ttl.
func HealthOf(w WorkerInfo, now time.Time, ttl time.Duration) Health {
	age := now.Sub(w.LastSeen)
	switch {
	case age > 3*ttl:
		return Lost
	case w.Draining:
		return Draining
	case age > ttl:
		return Late
	}
	return Healthy
}

var healthStatus = map[Health]ui.Status{Healthy: ui.StatusPass, Late: ui.StatusWait, Draining: ui.StatusInfo, Lost: ui.StatusFail}

type workerRow struct {
	ID, Pool, Labels, Caps, Job, Seen string
	Health                            ui.Chip
	CanDrain                          bool
}

type poolStat struct {
	Pool           string
	Healthy, Total int
	Stat           ui.Stat
}

type workersData struct {
	Off   bool
	Pools []poolStat
	Rows  []workerRow
}

func (c *Console) workers(w http.ResponseWriter, r *http.Request, _ contract.Principal) {
	var d workersData
	if c.cfg.Workers == nil {
		d.Off = true
		c.page.render(w, http.StatusOK, "workers", "Workers", notice(r), d)
		return
	}
	now, ttl := c.cfg.Now(), c.cfg.Workers.TTL()
	ws := c.cfg.Workers.List()
	slices.SortFunc(ws, func(a, b WorkerInfo) int { return strings.Compare(a.Pool+"/"+a.ID, b.Pool+"/"+b.ID) })
	pools := map[string]*poolStat{}
	for _, wk := range ws {
		h := HealthOf(wk, now, ttl)
		ps := pools[wk.Pool]
		if ps == nil {
			ps = &poolStat{Pool: wk.Pool}
			pools[wk.Pool] = ps
		}
		ps.Total++
		if h == Healthy {
			ps.Healthy++
		}
		var labels []string
		for k, v := range wk.Labels {
			if k != "pool" {
				labels = append(labels, k+"="+v)
			}
		}
		slices.Sort(labels)
		d.Rows = append(d.Rows, workerRow{ID: wk.ID, Pool: wk.Pool, Labels: strings.Join(labels, " "), Caps: or(strings.Join(wk.Caps, ", "), "–"),
			Job: or(wk.Job, "idle"), Seen: ago(now.Sub(wk.LastSeen)), Health: ui.Chip{Label: string(h), Status: healthStatus[h]},
			CanDrain: h == Healthy || h == Late})
	}
	for _, ps := range pools {
		st := ui.StatusPass
		switch {
		case ps.Healthy == 0:
			st = ui.StatusFail
		case ps.Healthy < ps.Total:
			st = ui.StatusWait
		}
		ps.Stat = ui.Stat{Label: "Pool " + ps.Pool, Value: itoa(ps.Healthy) + " of " + itoa(ps.Total) + " healthy", Status: st}
		d.Pools = append(d.Pools, *ps)
	}
	slices.SortFunc(d.Pools, func(a, b poolStat) int { return strings.Compare(a.Pool, b.Pool) })
	c.page.render(w, http.StatusOK, "workers", "Workers", notice(r), d)
}

func itoa(n int) string { return strconv.Itoa(n) }

// ago prints a duration as "4s ago" or "3m ago".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s ago"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	}
	return itoa(int(d.Hours())) + "h ago"
}

func (c *Console) drain(w http.ResponseWriter, r *http.Request, p contract.Principal) {
	id := r.PostFormValue("id")
	if c.cfg.Workers == nil || !slices.ContainsFunc(c.cfg.Workers.List(), func(wk WorkerInfo) bool { return wk.ID == id }) {
		c.page.error(w, http.StatusNotFound, "No such worker", "The worker may have left the pool. Reload the workers page.")
		return
	}
	if err := c.change(r.Context(), p, contract.ActionWorkerManage, map[string]string{"worker": id, "op": "drain"},
		func() error { return c.cfg.Workers.Drain(id) }); err != nil {
		c.fail(w, "The worker was not drained")
		return
	}
	done(w, r, "/admin/workers", "drained")
}
