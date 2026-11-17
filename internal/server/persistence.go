package server

import "sync"

// Browser tabs have independent Apps but share durable files. Lock the entire
// read/modify/write transaction by its owning path, rather than by App instance.
// App-local locks must always be acquired before this file lock.
var persistentFileLocks sync.Map // path -> *sync.RWMutex

func persistentFileLock(path string) *sync.RWMutex {
	lock, _ := persistentFileLocks.LoadOrStore(path, &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}
