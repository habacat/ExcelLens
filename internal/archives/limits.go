package archives

import (
	"errors"
	"io"

	"github.com/bodgit/sevenzip"
	"github.com/ulikunitz/xz/lzma"
)

// Bound LZMA2 dictionaries before allocation. Small header streams also do
// not need the entire dictionary advertised by the producing application.
func init() { sevenzip.RegisterDecompressor([]byte{0x21}, boundedLZMA2) }

func boundedLZMA2(properties []byte, size uint64, readers []io.ReadCloser) (io.ReadCloser, error) {
	if len(properties) != 1 || properties[0] > 40 || len(readers) != 1 {
		return nil, errors.New("无效的 7z LZMA2 参数")
	}
	dict := (uint64(2) | (uint64(properties[0]) & 1)) << (properties[0]/2 + 11)
	if size > 0 && size < dict {
		dict = size
	}
	if dict < 4096 {
		dict = 4096
	}
	if dict > 512<<20 {
		return nil, errors.New("7z 解码字典超过 512MB，请使用较小字典重新压缩")
	}
	r, err := (lzma.Reader2Config{DictCap: int(dict)}).NewReader2(readers[0])
	if err != nil {
		return nil, err
	}
	return &decoderCloser{Reader: r, Closer: readers[0]}, nil
}

type decoderCloser struct {
	io.Reader
	io.Closer
}

var decodeSlots = make(chan struct{}, 2)
