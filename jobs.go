package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// A background task for the extra tabs (merging, damaged files), with progress for the screen.
type Job struct {
	mu      sync.Mutex
	Running bool
	Cancel  atomic.Bool
	Count   atomic.Int64
	Total   atomic.Int64
	Current atomic.Value
	Result  any
	Err     string
	Started time.Time
}

var (
	jobsMu sync.Mutex
	jobs   = map[string]*Job{}
)

func getJob(kind string) *Job {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	j := jobs[kind]
	if j == nil {
		j = &Job{}
		jobs[kind] = j
	}
	return j
}

// startJob runs fn in the background unless that kind of job is already running.
func startJob(kind string, fn func(j *Job) (any, error)) bool {
	jobsMu.Lock()
	if old := jobs[kind]; old != nil {
		old.mu.Lock()
		running := old.Running
		old.mu.Unlock()
		if running {
			jobsMu.Unlock()
			return false
		}
	}
	j := &Job{Running: true, Started: time.Now()}
	jobs[kind] = j
	jobsMu.Unlock()
	go func() {
		res, err := fn(j)
		j.mu.Lock()
		j.Running, j.Result = false, res
		if err != nil {
			j.Err = err.Error()
		}
		j.mu.Unlock()
	}()
	return true
}

func (j *Job) status() map[string]any {
	j.mu.Lock()
	defer j.mu.Unlock()
	cur, _ := j.Current.Load().(string)
	out := map[string]any{"running": j.Running, "count": j.Count.Load(), "total": j.Total.Load(), "current": cur,
		"err": j.Err, "cancelled": j.Cancel.Load()}
	if !j.Running {
		out["result"] = j.Result
	}
	if !j.Started.IsZero() {
		out["seconds"] = int(time.Since(j.Started).Seconds())
	}
	return out
}
