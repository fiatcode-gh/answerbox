package exam

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"regexp"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// imageNameRe is the media handler's file name rule: only such names are
// ever served.
var imageNameRe = regexp.MustCompile(`^[0-9a-f]{32}\.(png|jpg|gif|webp)$`)

var errInterrupted = errors.New("interrupted")

// interruption is the rest of a reader cut off halfway: its first Read calls
// at, standing in for the moment a kill would hit, and fails.
type interruption struct{ at func() }

func (r interruption) Read([]byte) (int, error) {
	r.at()
	return 0, errInterrupted
}

func TestWriteImages_InterruptedWriteLeavesNoFinalFile(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	const key = "0123456789abcdef0123456789abcdef"
	if err := root.Mkdir(key, 0o750); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 200_000)
	rand.NewChaCha8([32]byte{7}).Read(data)
	copy(data, "\x89PNG\r\n\x1a\n")
	name, ok := examfile.ImageName(data)
	if !ok {
		t.Fatal("test image is not an image")
	}
	final := key + "/" + name

	// What a kill at that moment would leave: every file of the folder with
	// the bytes it holds then.
	left := map[string][]byte{}
	cut := io.MultiReader(bytes.NewReader(data[:len(data)/2]), interruption{at: func() {
		entries, err := fs.ReadDir(root.FS(), key)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := root.ReadFile(key + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			left[e.Name()] = b
		}
	}})
	if err := writeImage(root, final, cut); !errors.Is(err, errInterrupted) {
		t.Fatalf("interrupted write: err = %v, want %v", err, errInterrupted)
	}
	if len(left) == 0 {
		t.Fatal("no file was being written when the write was cut off")
	}
	for n := range left {
		if n == name || imageNameRe.MatchString(n) {
			t.Errorf("mid-write the folder holds %q, an image name", n)
		}
	}
	if _, err := root.Stat(final); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after the failed write Stat(final) = %v, want not exist", err)
	}
	if entries, err := fs.ReadDir(root.FS(), key); err != nil || len(entries) != 0 {
		t.Errorf("after the failed write the folder holds %v (err %v), want nothing", entries, err)
	}

	for n, b := range left {
		if err := root.WriteFile(key+"/"+n, b, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{media: root}
	created, err := s.writeImages(key, []examfile.Image{{Name: name, Data: data}})
	if err != nil {
		t.Fatalf("next write: %v", err)
	}
	if len(created) != 1 || created[0] != name {
		t.Errorf("created = %q, want [%q]", created, name)
	}
	if got, err := root.ReadFile(final); err != nil || !bytes.Equal(got, data) {
		t.Errorf("final file: %d bytes, err %v; want the %d bytes of the image", len(got), err, len(data))
	}
}
