package fileidentity

import "sync"

// Only a process-local receipt of a fully hashed source is memoized. Persistent
// caches never get to assert a source digest. Platforms without a reliable
// change-time/file-ID/revision stamp continue hashing every byte on each open.
type Stamp struct {
	Volume                           uint64
	ID                               [16]byte
	Created, Modified, Changed, Size int64
	Revision                         int64
}
type sourceFingerprintKey struct {
	stamp  Stamp
	format string
}
type sourceFingerprintReceipt struct {
	digest string
	used   uint64
}

var sourceFingerprints = struct {
	sync.Mutex
	tick  uint64
	items map[sourceFingerprintKey]sourceFingerprintReceipt
}{items: make(map[sourceFingerprintKey]sourceFingerprintReceipt)}

func Recall(stamp Stamp, format string) (string, bool) {
	sourceFingerprints.Lock()
	defer sourceFingerprints.Unlock()
	key := sourceFingerprintKey{stamp, format}
	receipt, ok := sourceFingerprints.items[key]
	if ok {
		sourceFingerprints.tick++
		receipt.used = sourceFingerprints.tick
		sourceFingerprints.items[key] = receipt
	}
	return receipt.digest, ok
}

func Remember(stamp Stamp, format, digest string) {
	sourceFingerprints.Lock()
	defer sourceFingerprints.Unlock()
	const limit = 64
	if len(sourceFingerprints.items) >= limit {
		var oldest sourceFingerprintKey
		used := ^uint64(0)
		for key, receipt := range sourceFingerprints.items {
			if receipt.used < used {
				oldest, used = key, receipt.used
			}
		}
		delete(sourceFingerprints.items, oldest)
	}
	sourceFingerprints.tick++
	sourceFingerprints.items[sourceFingerprintKey{stamp, format}] = sourceFingerprintReceipt{digest, sourceFingerprints.tick}
}
