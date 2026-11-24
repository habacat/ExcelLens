package localstore

import (
	"context"
	"io"
	"time"
)

type Progress struct {
	Phase string `json:"phase"`
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total"`
	Files int    `json:"files"`
	Items int    `json:"items"`
}
type TransferOptions struct {
	Password   string
	spaceProbe spaceProbe
	Report     func(Progress)
	// BeginCommit arbitrates publication with the process-wide cancellation endpoint.
	BeginCommit func() error
}

func (o TransferOptions) enterCommit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.BeginCommit != nil {
		return o.BeginCommit()
	}
	return nil
}

type meterKey struct{}
type meter struct {
	Progress
	report func(Progress)
	next   time.Time
}

func (o TransferOptions) start(ctx context.Context) context.Context {
	if o.Report == nil {
		return ctx
	}
	return context.WithValue(ctx, meterKey{}, &meter{report: o.Report})
}
func phase(ctx context.Context, name string, total int64, items int) {
	if m, _ := ctx.Value(meterKey{}).(*meter); m != nil {
		m.Progress = Progress{Phase: name, Total: total, Items: items}
		m.notify(true)
	}
}
func (m *meter) notify(force bool) {
	if now := time.Now(); force || !now.Before(m.next) {
		m.report(m.Progress)
		m.next = now.Add(150 * time.Millisecond)
	}
}
func finishFile(ctx context.Context) {
	if m, _ := ctx.Value(meterKey{}).(*meter); m != nil {
		m.Files++
		m.notify(m.Items > 0 && m.Files == m.Items)
	}
}

type progressReader struct {
	cancelledReader
	m *meter
}

func meteredReader(ctx context.Context, src io.Reader) io.Reader {
	reader := cancelledReader{ctx, src}
	if m, _ := ctx.Value(meterKey{}).(*meter); m != nil {
		return progressReader{reader, m}
	}
	return reader
}
func (r progressReader) Read(b []byte) (int, error) {
	n, err := r.cancelledReader.Read(b)
	r.m.Bytes += int64(n)
	r.m.notify(false)
	return n, err
}
