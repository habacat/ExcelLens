package transferjobs

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"excellens/internal/savedconfig"
)

const historyBytes = 1 << 20

type HistoryIssue struct {
	Reason   string `json:"reason"`
	Revision string `json:"revision"`
}

func historyFile(path string) (*os.File, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("传输历史不是普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	current, err := f.Stat()
	if err != nil || !os.SameFile(st, current) {
		f.Close()
		return nil, errors.New("传输历史文件已更换")
	}
	return f, nil
}
func inspect(file string) ([]Job, *HistoryIssue, error) {
	if st, err := os.Lstat(file); err == nil && !st.Mode().IsRegular() {
		return nil, nil, errors.New("传输历史不是普通文件")
	}
	var jobs []Job
	found, err := savedconfig.Read(file, historyBytes, &jobs)
	if err == nil && found {
		if jobs == nil || len(jobs) > 32 {
			err = fmt.Errorf("%w：传输记录清单无效", savedconfig.ErrDamaged)
		}
		seen := map[string]bool{}
		for _, j := range jobs {
			if j.ID == "" || seen[j.ID] || j.Action == "" || j.Started.IsZero() || j.State == "" {
				err = fmt.Errorf("%w：传输记录字段无效", savedconfig.ErrDamaged)
				break
			}
			seen[j.ID] = true
		}
	}
	if !errors.Is(err, savedconfig.ErrDamaged) {
		return jobs, nil, err
	}
	f, e := historyFile(file)
	if e != nil {
		return nil, nil, e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return nil, nil, e
	}
	return nil, &HistoryIssue{Reason: err.Error(), Revision: fmt.Sprintf("%x", h.Sum(nil))}, nil
}
func (s *Manager) load(file string) {
	if s.loaded {
		return
	}
	jobs, issue, err := inspect(file)
	if err != nil {
		// A future format or inaccessible path cannot be safely repaired.
		// Preserve it and keep this process's transfers available in memory.
		issue = &HistoryIssue{Reason: err.Error()}
		jobs = nil
	}
	for i := range jobs {
		if jobs[i].State == "running" {
			jobs[i].State = "interrupted"
			jobs[i].Transaction = "finished"
			jobs[i].Error = "程序退出前未记录完成，请核对目标后重试"
		}
	}
	s.jobs, s.issue, s.loaded = jobs, issue, true
}
func (s *Manager) save(file string, jobs []Job) {
	if s.issue != nil {
		// Keep new receipts in memory until the user confirms a repair.
		return
	}
	_, issue, err := inspect(file)
	if issue != nil {
		s.issue = issue
		return
	}
	if err == nil {
		err = savedconfig.Write(file, historyBytes, jobs, savedconfig.Durable)
	}
	if err != nil {
		s.issue = &HistoryIssue{Reason: err.Error()}
	}
}

// Repair takes an explicit confirmation bound to the bytes the user was shown.
// A failed evidence copy or replacement leaves the existing history in place.
func (s *Manager) Repair(file, revision string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.done != nil {
		return "", errors.New("请在传输结束后重建历史")
	}
	_, issue, err := inspect(file)
	if err != nil {
		s.issue = &HistoryIssue{Reason: err.Error()}
		return "", err
	}
	s.issue = issue
	if issue == nil || revision == "" || issue.Revision != revision {
		return "", errors.New("传输历史已变化，请刷新后核对")
	}
	f, err := historyFile(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	evidence, err := os.CreateTemp(filepath.Dir(file), "transfer-history-damaged-*.json")
	if err != nil {
		return "", err
	}
	path := evidence.Name()
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(evidence, h), f)
	if err == nil {
		err = evidence.Sync()
	}
	err = errors.Join(err, evidence.Close())
	if err != nil || fmt.Sprintf("%x", h.Sum(nil)) != revision {
		os.Remove(path)
		if err != nil {
			return "", err
		}
		return "", errors.New("传输历史在备份诊断文件时发生变化")
	}
	if err = f.Close(); err != nil {
		return path, err
	}
	_, latest, err := inspect(file)
	if err != nil {
		return path, err
	}
	if latest == nil || latest.Revision != revision {
		return path, errors.New("传输历史已变化，诊断副本已保留")
	}
	if err = savedconfig.Write(file, historyBytes, append([]Job{}, s.jobs...), savedconfig.Durable); err != nil {
		return path, err
	}
	s.loaded, s.issue = true, nil
	return path, nil
}
