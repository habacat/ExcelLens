package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

type Change struct {
	Key  string          `json:"key"`
	Data json.RawMessage `json:"data"`
}

type Request struct {
	Owner     string   `json:"owner"`
	Kind      string   `json:"kind"`
	Revision  uint64   `json:"revision"`
	RequestID string   `json:"requestId"`
	Changes   []Change `json:"changes"`
}

type Meta struct {
	Revision  uint64 `json:"revision"`
	Recovered bool   `json:"recovered,omitempty"`
}

type Snapshot struct {
	Meta
	Records map[string]json.RawMessage `json:"records"`
}

type Entry struct {
	Owner   string    `json:"owner"`
	Kind    string    `json:"kind"`
	Updated time.Time `json:"updated"`
	Bytes   int64     `json:"bytes"`
	Backup  bool      `json:"backup,omitempty"`
}

type frame struct {
	Revision   uint64     `json:"revision"`
	RequestID  string     `json:"requestId,omitempty"`
	Changes    changeList `json:"changes"`
	Checkpoint bool       `json:"checkpoint,omitempty"`
	Recent     recentList `json:"recent,omitempty"`
	Digest     string     `json:"digest"`
}

type recentList []string

func (ids *recentList) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('[') {
		return ErrCorrupt
	}
	for d.More() {
		if len(*ids) >= 64 {
			return ErrCorrupt
		}
		var id string
		if err := d.Decode(&id); err != nil || !validID(id) {
			return ErrCorrupt
		}
		*ids = append(*ids, id)
	}
	_, err = d.Token()
	return err
}

type changeList []Change

func (c *changeList) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('[') {
		return ErrCorrupt
	}
	for d.More() {
		if len(*c) >= MaxRecords {
			return ErrCorrupt
		}
		var item Change
		if err := d.Decode(&item); err != nil {
			return err
		}
		if err := validateChange(item); err != nil {
			return err
		}
		*c = append(*c, item)
	}
	_, err = d.Token()
	return err
}

func validateChange(c Change) error {
	if len(c.Key) == 0 || len(c.Key) > 1024 || len(c.Data) > MaxRecordBytes || (len(c.Data) > 0 && !json.Valid(c.Data)) {
		return errors.New("恢复记录内容超限或无效")
	}
	return nil
}

func charge(key string, data []byte) int {
	if len(data) == 0 {
		return 0
	}
	encoded, _ := json.Marshal(key)
	return len(encoded) + len(data) + 128
}

func sortedChanges(changes map[string]json.RawMessage) changeList {
	items := make(changeList, 0, len(changes))
	for key, data := range changes {
		items = append(items, Change{key, data})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}

func digest(items changeList) string {
	encoded, _ := marshalJournal(items)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func marshalJournal(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	// These are JSON frames, not HTML. Escaping RawMessage again can multiply
	// its encoded size and make an accepted record fail its own replay limit.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}
