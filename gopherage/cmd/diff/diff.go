/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package diff

import (
	"github.com/spf13/cobra"
	"golang.org/x/tools/cover"
	"k8s.io/test-infra/gopherage/pkg/cov"
	"k8s.io/test-infra/gopherage/pkg/util"
	"log"
)

type flags struct {
	OutputFile string
}

// MakeCommand returns a `diff` command.
func MakeCommand() *cobra.Command {
	flags := &flags{}
	cmd := &cobra.Command{
		Use:   "diff [first] [second]",
		Short: "Diffs two Go coverage files.",
		Long: `Takes the difference between two Go coverage files, producing another Go coverage file
showing only what was covered between the two files being generated. This works best when using
files generated in "count" or "atomic" mode; "set" may drastically underreport.

It is assumed that both files came from the same execution, and so all values in the second file are
at least equal to those in the first file.`,
		Run: func(cmd *cobra.Command, args []string) {
			run(flags, cmd, args)
		},
	}
	cmd.Flags().StringVar(&flags.OutputFile, "o", "-", "output file")
	return cmd
}

func run(flags *flags, cmd *cobra.Command, args []string) {
	if len(args) != 2 {
		log.Fatal("Expected exactly two arguments.")
	}

	before, err := cover.ParseProfiles(args[0])
	if err != nil {
		log.Fatalf("couldn't load %s: %v", args[0], err)
	}

	after, err := cover.ParseProfiles(args[1])
	if err != nil {
		log.Fatalf("couldn't load %s: %v", args[0], err)
	}

	diff, err := cov.DiffProfiles(before, after)
	if err != nil {
		log.Fatalf("failed to diff profiles: %v", err)
	}

	if err := util.DumpProfile(flags.OutputFile, diff); err != nil {
		log.Fatalln(err)
	}
}
