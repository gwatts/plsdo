/*
Copyright © 2024 Gareth Watts <gareth@omnipotent.net>
*/
package cmd

import (
	"fmt"
	"os"

	"github.com/gwatts/plsdo/pkg/plsdo"
	"github.com/spf13/cobra"
)

const (
	fmtJson   = "json"
	fmtPretty = "print"
)

var (
	format string
	style  string
	debug  bool
)

// refsCmd represents the refs command
var refsCmd = &cobra.Command{
	Use:   "refs <package> <pattern> [pattern...]",
	Short: "Finds and prints references to specific function or method",
	Long:  `Accepts one or more patterns; can be a function name, or a type.method spec`,
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		// find specified method locations

		m, err := plsdo.NewMatcher()
		if debug {
			m.DebugWriter = os.Stderr
		}
		defer m.Close()
		cobra.CheckErr(err)

		pkgPath, patterns := args[0], args[1:]
		cobra.CheckErr(m.FindFuncReferences(pkgPath, patterns...))

		switch format {
		case fmtJson:
			cobra.CheckErr(m.Json(os.Stdout))
		case fmtPretty:
			m.PrettyPrint(os.Stdout, style)
		default:
			fmt.Fprintln(os.Stderr, "invalid mode")
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(refsCmd)

	refsCmd.Flags().StringVarP(&format, "fmt", "f", "print", "Output format")
	refsCmd.Flags().StringVarP(&style, "style", "s", "github-dark", "Output style")
	refsCmd.Flags().BoolVarP(&debug, "debug", "d", false, "Emit debug information to stderr")
}
