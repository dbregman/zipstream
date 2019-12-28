package zipstream

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"testing"
)

func Test(t *testing.T) {
	dir := "testdata"
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Error(err)
	}

	for _, file := range files {
		reader, err := os.Open(path.Join(dir, file.Name()))
		if err != nil {
			t.Errorf("Could not open '%s': %v", file, err)
		}
		fmt.Println(file.Name())
		r := NewReader(reader)
		for {
			f, err := r.Next()
			if err == io.EOF {
				remaining, copyErr := io.Copy(ioutil.Discard, r.Buffered())
				fmt.Printf("\tremaining after EOF: %d (err=%v)\n", remaining, copyErr)
				break
			} else if err != nil {
				fmt.Printf("\tERROR:%v\n", err)
				break
			}
			fmt.Printf("\t%s (size %d)\n", f.Name, f.UncompressedSize64)
		}
	}
}
