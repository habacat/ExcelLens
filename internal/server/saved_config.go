package server

import (
	"os"
	"path/filepath"

	"excellens/internal/localstore"
	"excellens/internal/savedconfig"
)

const maxConfigRevision = 1<<53 - 1

func savedConfigPath(kind, name string) (string, error) {
	root, err := localstore.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, kind, name+".json"), nil
}

func decodeSavedConfig(data []byte, dst any) error { return savedconfig.Decode(data, dst) }
func readSavedConfig(path string, maxBytes int64, dst any) (bool, error) {
	return savedconfig.Read(path, maxBytes, dst)
}

type configWriteOps struct {
	sync   func(*os.File) error
	rename func(string, string) error
}

var durableConfigOps = configWriteOps{(*os.File).Sync, os.Rename}

func writeSavedConfig(path string, maxBytes int64, value any, ops configWriteOps) error {
	return savedconfig.Write(path, maxBytes, value, savedconfig.WriteOps{Sync: ops.sync, Rename: ops.rename})
}
