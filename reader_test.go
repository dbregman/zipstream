package zipstream

import (
	"archive/zip"
	"bytes"
	"io"
	"io/ioutil"
	"math/rand"
	"testing"
)

func TestReader(t *testing.T) {
	testReader(t, []byte(`<poc><firstName>Juan</firstName></poc>`))

	s := new(bytes.Buffer)
	io.Copy(s, io.LimitReader(rand.New(rand.NewSource(1)), 16384))
	testReader(t, s.Bytes())
}

func testReader(t *testing.T, s []byte) {
	var wbuf bytes.Buffer
	for j := 0; j < 2; j++ {
		z := zip.NewWriter(&wbuf)
		for i := 0; i < 2; i++ {
			zw, err := z.Create("tmp")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := zw.Write(s); err != nil {
				t.Fatal(err)
			}
		}

		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	}

	zr := NewReader(&wbuf)
	for j := 0; j < 2; j++ {
		fcount := 0
		for {
			_, err := zr.Next()
			if err != nil {
				if err != io.EOF {
					t.Fatal(err)
				}
				if fcount != 2 {
					t.Fatal("Embedded file missing", j, fcount, err)
				}
				break // No more files
			}
			fcount++
			s2, err := ioutil.ReadAll(zr)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Compare(s, s2) != 0 {
				t.Fatal("Decompressed data does not match original")
			}
		}
	}
}
