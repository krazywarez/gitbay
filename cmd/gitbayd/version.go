package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/buildinfo"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the commit this binary was built from",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(buildinfo.String())
			return nil
		},
	}
}
