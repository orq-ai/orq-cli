package commands

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

// NewManPagesCommand generates man pages for the full command tree. Hidden:
// it exists for the release pipeline (`make man`), not for end users, who get
// the result via their package manager.
func NewManPagesCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:    "man-pages",
		Short:  "Generate man pages for all commands into a directory",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			header := &doc.GenManHeader{
				Title:   "ORQ",
				Section: "1",
				Source:  "orq " + cmd.Root().Version,
				Manual:  "orq.ai CLI Manual",
			}
			return doc.GenManTree(cmd.Root(), header, dir)
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "man", "Output directory for the generated pages")
	return cmd
}
