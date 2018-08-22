package artifacts

import (
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/sirupsen/logrus"
	"io"
	"k8s.io/test-infra/coverage/logUtil"
)

type LocalArtifacts struct {
	Artifacts
}

func NewLocalArtifacts(directory string, ProfileName string,
	KeyProfileName string, CovStdoutName string) *LocalArtifacts {
	return &LocalArtifacts{*New(
		directory,
		ProfileName,
		KeyProfileName,
		CovStdoutName)}
}

// ProfileReader create and returns a ProfileReader by opening the file stored in profile path
func (arts *LocalArtifacts) ProfileReader() io.ReadCloser {
	f, err := os.Open(arts.ProfilePath())
	if err != nil {
		wd, _ := os.Getwd()
		logUtil.LogFatalf("LocalArtifacts.ProfileReader(): os.Open(profilePath) error: %v, cwd=%s", err, wd)
	}
	return f
}

func (arts *LocalArtifacts) ProfileName() string {
	return arts.profileName
}

// KeyProfileCreator creates a key profile file that will be used to hold a
// filtered version of coverage profile that only stores the entries that
// will be displayed by line coverage tool
func (arts *LocalArtifacts) KeyProfileCreator() *os.File {
	keyProfilePath := arts.KeyProfilePath()
	keyProfileFile, err := os.Create(keyProfilePath)
	logrus.Infof("os.Create(keyProfilePath)=%s", keyProfilePath)
	if err != nil {
		logUtil.LogFatalf("file(%s) creation error: %v", keyProfilePath, err)
	}

	return keyProfileFile
}

// ProduceProfileFile produce coverage profile (&its stdout) by running go test on target package
// for periodic job, produce junit xml for testgrid in addition
func (arts *LocalArtifacts) ProduceProfileFile(covTargetsStr string) {
	// creates artifacts directory
	logrus.Infof("mkdir -p %s\n", arts.directory)
	cmd := exec.Command("mkdir", "-p", arts.directory)
	logrus.Infof("artifacts dir=%s\n", arts.directory)
	cmd.Run()

	// convert targets from a single string to a lists of strings
	var covTargets []string
	for _, target := range strings.Split(covTargetsStr, " ") {
		covTargets = append(covTargets, "./"+path.Join(target, "..."))
	}
	logrus.Infof("covTargets = %v\n", covTargets)

	runProfiling(covTargets, arts)
}
