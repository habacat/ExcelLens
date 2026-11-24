// Package transferjobs owns process-wide transfer admission, publication state and history.
package transferjobs

import (
	"context"
	"errors"
	"sync"
	"time"

	"excellens/internal/localstore"
)

type Result struct {
	Message   string `json:"message,omitempty"`
	Candidate string `json:"candidate,omitempty"`
}
type Job struct {
	Result
	ID          string              `json:"id"`
	Action      string              `json:"action"`
	State       string              `json:"state"`
	Transaction string              `json:"transaction"`
	Progress    localstore.Progress `json:"progress"`
	Error       string              `json:"error,omitempty"`
	Started     time.Time           `json:"started"`
	Finished    time.Time           `json:"finished,omitzero"`
}
type Manager struct {
	mu             sync.Mutex
	jobs           []Job
	loaded, closed bool
	issue          *HistoryIssue
	cancel         context.CancelFunc
	done           chan struct{}
}

func (s *Manager) Stop() {
	s.mu.Lock()
	s.closed = true
	if s.cancel != nil && s.jobs[0].Transaction != "committing" {
		s.jobs[0].Transaction = "cancelling"
		s.cancel()
	}
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}
func (s *Manager) Start(file, id, action string, run func(context.Context, localstore.TransferOptions) (Result, error), finished func()) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var empty Job
	if s.closed {
		return empty, errors.New("程序正在退出")
	}
	if s.done != nil {
		return empty, errors.New("另一项数据传输正在进行")
	}
	s.load(file)
	job := Job{ID: id, Action: action, State: "running", Transaction: "preparing", Started: time.Now().UTC(), Progress: localstore.Progress{Phase: "准备"}}
	jobs := append([]Job{job}, s.jobs[:min(len(s.jobs), 31)]...)
	s.save(file, jobs)
	ctx, cancel := context.WithCancel(context.Background())
	s.jobs, s.cancel, s.done = jobs, cancel, make(chan struct{})
	go func() {
		defer cancel()
		s.mu.Lock()
		if s.jobs[0].Transaction == "preparing" {
			s.jobs[0].Transaction = "cancellable"
		}
		s.mu.Unlock()
		options := localstore.TransferOptions{
			Report: func(p localstore.Progress) { s.mu.Lock(); s.jobs[0].Progress = p; s.mu.Unlock() },
			BeginCommit: func() error {
				s.mu.Lock()
				defer s.mu.Unlock()
				if err := ctx.Err(); err != nil {
					return err
				}
				s.jobs[0].Transaction = "committing"
				return nil
			},
		}
		result, err := run(ctx, options)
		s.mu.Lock()
		current := &s.jobs[0]
		if current.Transaction == "cancelling" && err == nil {
			err = context.Canceled
		}
		current.Transaction = "finished"
		current.Finished = time.Now().UTC()
		if err != nil {
			current.State, current.Error = "failed", err.Error()
			if errors.Is(err, context.Canceled) {
				current.State = "cancelled"
				if err == context.Canceled {
					current.Error = "已取消"
				}
			}
		} else {
			current.State, current.Result = "done", result
		}
		s.save(file, s.jobs)
		close(s.done)
		s.cancel, s.done = nil, nil
		finished()
		s.mu.Unlock()
	}()
	return job, nil
}

func (s *Manager) List(file string) ([]Job, *HistoryIssue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load(file)
	return append([]Job{}, s.jobs...), s.issue
}
func (s *Manager) Cancel(file, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load(file)
	for _, job := range s.jobs {
		if job.ID == id && job.State == "cancelled" {
			return nil
		}
	}
	if len(s.jobs) == 0 || id == "" || id != s.jobs[0].ID || s.cancel == nil {
		return errors.New("任务已结束或不存在")
	}
	if s.jobs[0].Transaction == "committing" {
		return errors.New("已进入提交阶段，无法取消")
	}
	s.jobs[0].Transaction = "cancelling"
	s.cancel()
	return nil
}
