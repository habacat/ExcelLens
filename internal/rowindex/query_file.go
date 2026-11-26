package rowindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"excellens/internal/formats"
)

// QueryQuota bounds temporary disposable query files across the process.
// Count both sort runs and retained result mappings against the same disk budget.
type QueryQuota struct {
	used    atomic.Int64
	max     int64
	filesMu sync.Mutex
	files   map[string]*QueryFile
}

var processQueryQuota = QueryQuota{max: formats.MaxQueryTempBytes}

func DefaultQueryQuota() *QueryQuota { return &processQueryQuota }

func NewQueryQuota(maxBytes int64) *QueryQuota { return &QueryQuota{max: maxBytes} }

func (q *QueryQuota) Used() int64 { return q.used.Load() }

// QueryFilesInUse protects live temporary files and mapped query results
// belonging to this process or other live local processes.
func QueryFilesInUse(dir string) bool {
	root := queryFileKey(dir)
	processQueryQuota.filesMu.Lock()
	for key, file := range processQueryQuota.files {
		if !file.retired.Load() && (key == root || strings.HasPrefix(key, root+string(filepath.Separator))) {
			processQueryQuota.filesMu.Unlock()
			return true
		}
	}
	processQueryQuota.filesMu.Unlock()
	// Other Windows logon sessions and developer processes can share a cache.
	// Closed sort runs are still live until their owning process releases them.
	return queryDirectoryInUse(dir) || queryDirectoryInUse(filepath.Join(dir, "queries"))
}

func queryFileOwner(name string) (int, bool) {
	if !strings.HasPrefix(name, ".query-p") || !strings.HasSuffix(name, ".tmp") {
		return 0, false
	}
	pidText, _, ok := strings.Cut(strings.TrimPrefix(name, ".query-p"), "-")
	pid, err := strconv.ParseUint(pidText, 10, 32)
	return int(pid), ok && err == nil && pid > 0
}

func queryDirectoryInUse(dir string) bool {
	f, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true // uncertainty must not permit deleting another process's run
	}
	defer f.Close()
	for {
		entries, err := f.ReadDir(128)
		for _, entry := range entries {
			if pid, ok := queryFileOwner(entry.Name()); ok && pid != os.Getpid() && queryProcessAlive(pid) {
				return true
			}
		}
		if err != nil {
			return err != io.EOF
		}
	}
}

func (q *QueryQuota) reserve(n int64) error {
	for {
		old := q.used.Load()
		if n < 0 || n > q.max-old {
			return errors.New("查询临时文件达到共享空间预算，请关闭旧结果或缩小范围")
		}
		if q.used.CompareAndSwap(old, old+n) {
			return nil
		}
	}
}

// QueryFile is a quota-accounted temporary file owned by a single query or order.
type QueryFile struct {
	file            *os.File
	path            string
	size            int64
	quota           *QueryQuota
	retired         atomic.Bool
	removeMu        sync.Mutex
	cleanupReported atomic.Bool
}

func (f *QueryFile) File() *os.File { return f.file }
func (f *QueryFile) Path() string   { return f.path }
func (f *QueryFile) Size() int64    { return f.size }

func queryFileKey(path string) string {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// CleanStale removes leftover query files from crashed processes while keeping
// active files in this process and other live processes protected.
func (q *QueryQuota) CleanStale(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.filesMu.Lock()
	retired := make([]*QueryFile, 0)
	for _, f := range q.files {
		if f.retired.Load() {
			retired = append(retired, f)
		}
	}
	q.filesMu.Unlock()
	for _, f := range retired {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := f.Discard(); err != nil && !f.cleanupReported.Swap(true) {
			// Keep charging a file we cannot remove. It must not block an
			// unrelated query that still fits the remaining shared budget.
			log.Printf("清理已结束的查询文件失败：%v", err)
		}
	}
	q.filesMu.Lock()
	defer q.filesMu.Unlock()
	directory, err := os.Open(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := directory.ReadDir(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".query-") || !strings.HasSuffix(entry.Name(), ".tmp") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if q.files[queryFileKey(path)] != nil {
				continue
			}
			if pid, owned := queryFileOwner(entry.Name()); owned && queryProcessAlive(pid) {
				continue
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("清理上次中断的查询文件失败：%w", err)
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// OpenTempFile cleans stale runs in dir and allocates a new file against the
// default process-wide query quota.
func OpenTempFile(ctx context.Context, dir string) (*QueryFile, error) {
	if err := processQueryQuota.CleanStale(ctx, dir); err != nil {
		return nil, err
	}
	return newQueryFile(dir, &processQueryQuota)
}

func newQueryFile(dir string, quota *QueryQuota) (*QueryFile, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	quota.filesMu.Lock()
	defer quota.filesMu.Unlock()
	if len(quota.files) >= formats.MaxQueryTempFiles {
		return nil, errors.New("查询临时文件数量达到共享预算，请关闭旧结果或缩小范围")
	}
	f, err := os.CreateTemp(dir, fmt.Sprintf(".query-p%d-*.tmp", os.Getpid()))
	if err != nil {
		return nil, err
	}
	result := &QueryFile{file: f, path: f.Name(), quota: quota}
	if quota.files == nil {
		quota.files = make(map[string]*QueryFile)
	}
	quota.files[queryFileKey(result.path)] = result
	return result, nil
}

// All writes append to new files. Header replacement uses WriteAt and does
// not reserve twice for the same bytes. A failed/short write releases its tail.
func (f *QueryFile) Write(p []byte) (int, error) {
	if err := f.quota.reserve(int64(len(p))); err != nil {
		return 0, err
	}
	n, err := f.file.Write(p)
	f.size += int64(n)
	f.quota.used.Add(int64(n - len(p)))
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (f *QueryFile) Close() error {
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func (f *QueryFile) Discard() error {
	f.removeMu.Lock()
	defer f.removeMu.Unlock()
	closeErr := f.Close()
	f.retired.Store(true)
	err := os.Remove(f.path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		f.quota.used.Add(-f.size)
		f.size = 0
		f.quota.filesMu.Lock()
		delete(f.quota.files, queryFileKey(f.path))
		f.quota.filesMu.Unlock()
		return closeErr
	}
	return err // Keep charging space we could not remove.
}
