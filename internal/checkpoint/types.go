// Package checkpoint stores explicitly resumable query/export stages. It does
// not parse source files, decide business record boundaries or run any task.
package checkpoint

import (
	"encoding/json"
	"errors"
	"time"
)

const Schema = 1

var (
	ErrBusy      = errors.New("恢复任务正在使用，请稍后重试")
	ErrCorrupt   = errors.New("任务检查点校验失败，原副本已保留")
	ErrCapacity  = errors.New("任务恢复空间或数量已达上限，请先清理不再需要的任务")
	ErrConflict  = errors.New("任务检查点版本已变化，请刷新后重试")
	ErrBinding   = errors.New("任务来源或规则与检查点不一致，请重新运行")
	ErrClosed    = errors.New("任务检查点已经关闭")
	ErrUncertain = errors.New("检查点确认未完成，请重新核对恢复状态")
)

// Source binds both path and content identity. The business driver must check
// them against the actual source again before it resumes or publishes output.
type Source struct {
	Path        string `json:"path"`
	Book        string `json:"book"`
	Fingerprint string `json:"fingerprint"`
	Format      string `json:"format"`
	Source      string `json:"source"`
	Stamp       string `json:"stamp"`
}

// Definition is immutable. Payload contains declarative, driver-validated
// rules/ranges, never executable code or borrowed in-memory task identifiers.
type Definition struct {
	Kind      string          `json:"kind"`
	Driver    string          `json:"driver"`
	Semantics uint32          `json:"semantics"`
	Title     string          `json:"title"`
	FileName  string          `json:"fileName,omitempty"`
	Sources   []Source        `json:"sources"`
	Payload   json.RawMessage `json:"payload"`
}

type Manifest struct {
	Version        int             `json:"version"`
	ID             string          `json:"id"`
	Revision       uint64          `json:"revision,string"`
	Definition     Definition      `json:"definition"`
	DefinitionHash string          `json:"definitionHash"`
	Created        time.Time       `json:"created"`
	Updated        time.Time       `json:"updated"`
	Stage          string          `json:"stage"`
	Cursor         json.RawMessage `json:"cursor"`
	Records        int64           `json:"records,string"`
	Blocks         uint64          `json:"blocks,string"`
	DataBytes      int64           `json:"dataBytes,string"`
	ChainHash      string          `json:"chainHash"`
	Complete       bool            `json:"complete"`
}

// Advance acknowledges only complete driver records/stages. A zero-size,
// zero-record advance commits a cursor/stage without appending a data block.
type Advance struct {
	Revision uint64
	Stage    string
	Cursor   json.RawMessage
	Records  int64
	Bytes    int64
	Complete bool
}

type Block struct {
	Sequence uint64
	Records  int64
	Bytes    int64
}

type Entry struct {
	ID       string    `json:"id"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Busy     bool      `json:"busy"`
	Backup   bool      `json:"backup,omitempty"`
	Error    string    `json:"error,omitempty"`
}
