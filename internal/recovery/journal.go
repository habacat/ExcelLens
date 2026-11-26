package recovery

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
)

// Only the write boundary is replaceable, so tests can exercise a failed Sync
// or checkpoint installation without changing the durable read protocol.
type journalWriter interface {
	io.Writer
	Sync() error
	Close() error
}

func read(path string) (*state, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 8 || info.Size() > maxLog {
		return nil, ErrCorrupt
	}
	header := make([]byte, 8)
	if _, err = io.ReadFull(f, header); err != nil || string(header) != magic {
		return nil, ErrCorrupt
	}
	v := &state{Snapshot: Snapshot{Records: map[string]json.RawMessage{}}, fileSize: info.Size(), modified: info.ModTime()}
	good := int64(8)
	for {
		var prefix [8]byte
		n, e := io.ReadFull(f, prefix[:])
		if e == io.EOF {
			return v, nil
		}
		if e == io.ErrUnexpectedEOF {
			return repairTail(path, v, good, append([]byte(nil), prefix[:n]...))
		}
		if e != nil {
			return nil, e
		}
		length := binary.LittleEndian.Uint32(prefix[:])
		if length == 0 || length > maxFrame {
			return nil, ErrCorrupt
		}
		payload := make([]byte, int(length))
		n, e = io.ReadFull(f, payload)
		if e == io.ErrUnexpectedEOF || e == io.EOF {
			return repairTail(path, v, good, append(prefix[:], payload[:n]...))
		}
		if e != nil {
			return nil, e
		}
		if crc32.ChecksumIEEE(payload) != binary.LittleEndian.Uint32(prefix[4:]) {
			return nil, ErrCorrupt
		}
		var item frame
		if json.Unmarshal(payload, &item) != nil || item.Revision == 0 || item.Revision > MaxRevision ||
			(!item.Checkpoint && (item.Revision != v.Revision+1 || !validID(item.RequestID) || len(item.Recent) != 0)) ||
			(item.Checkpoint && (good != 8 || item.RequestID != "")) {
			return nil, ErrCorrupt
		}
		if item.Checkpoint {
			if len(item.Digest) != 64 {
				return nil, ErrCorrupt
			}
			v.recent = append([]string(nil), item.Recent...)
			if len(v.recent) > 64 {
				return nil, ErrCorrupt
			}
		}
		if !item.Checkpoint && item.Digest != digest(item.Changes) {
			return nil, ErrCorrupt
		}
		for _, c := range item.Changes {
			if err := validateChange(c); err != nil {
				return nil, ErrCorrupt
			}
			v.bytes -= charge(c.Key, v.Records[c.Key])
			if len(c.Data) == 0 || bytes.Equal(c.Data, []byte("null")) {
				delete(v.Records, c.Key)
			} else {
				v.Records[c.Key] = c.Data
				v.bytes += charge(c.Key, c.Data)
			}
		}
		if v.bytes > MaxDataBytes || len(v.Records) > MaxRecords {
			return nil, ErrCorrupt
		}
		v.Revision = item.Revision
		v.digest = item.Digest
		if item.RequestID != "" {
			v.recent = append(v.recent, item.RequestID)
			if len(v.recent) > 64 {
				v.recent = v.recent[len(v.recent)-64:]
			}
		}
		good += 8 + int64(length)
	}
}

func repairTail(path string, v *state, good int64, tail []byte) (*state, error) {
	// Preserve the incomplete bytes before truncating only the uncommitted tail.
	backup := fmt.Sprintf("%s.incomplete-%08x", path, crc32.ChecksumIEEE(tail))
	f, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		var n int
		n, err = f.Write(tail)
		if err == nil && n != len(tail) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	} else if errors.Is(err, os.ErrExist) {
		existing, e := os.ReadFile(backup)
		if e == nil && bytes.Equal(existing, tail) {
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	f, err = os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Truncate(good)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	v.Recovered = true
	v.fileSize = good
	if info, e := os.Stat(path); e == nil {
		v.modified = info.ModTime()
	}
	return v, nil
}

func writeFrame(w io.Writer, payload []byte) error {
	var p [8]byte
	binary.LittleEndian.PutUint32(p[:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(p[4:], crc32.ChecksumIEEE(payload))
	if n, err := w.Write(p[:]); err != nil {
		return err
	} else if n != len(p) {
		return io.ErrShortWrite
	}
	n, err := w.Write(payload)
	if err == nil && n != len(payload) {
		err = io.ErrShortWrite
	}
	return err
}

func (s *Store) checkpoint(path string, v *state) error {
	payload, err := marshalJournal(frame{Revision: v.Revision, Changes: sortedChanges(v.Records), Checkpoint: true, Recent: v.recent, Digest: v.digest})
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return errors.New("恢复检查点超过安全上限")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".recovery-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = io.WriteString(f, magic)
	}
	if err == nil {
		err = writeFrame(f, payload)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err = os.Stat(path); err == nil {
		if err = os.Remove(path + ".bak"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = s.rename(path, path+".bak"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = s.rename(f.Name(), path); err != nil {
		_ = s.rename(path+".bak", path)
	}
	return err
}
