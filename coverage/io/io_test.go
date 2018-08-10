package io

import (
	"io/ioutil"
	"log"
	"path"
	"testing"
	"github.com/kubernetes/test-infra/coverage/test"
)

func TestWriteToArtifacts(t *testing.T) {
	s := "content to be written on disk"
	artsDir := test.NewArtsDir("TestWriteToArtifacts")
	Write(&s, artsDir, "testWriteToArt.txt")
	content, err := ioutil.ReadFile(path.Join(artsDir, "testWriteToArt.txt"))
	if err != nil {
		log.Fatalf("Cannot read file, err = %v", err)
	}

	test.AssertEqual(t, s, string(content))

	test.DeleteDir(artsDir)
}
