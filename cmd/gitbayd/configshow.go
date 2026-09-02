package main

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
)

// configShowCmd prints the configuration the daemon runs with: the file
// with every default filled in, as TOML, so a support question starts
// from what is in effect rather than what was written. The SMTP password
// is the one secret the file can hold and is never printed.
func configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "print the effective configuration, defaults filled in, secrets redacted",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil && cfg.Server.Root == "" {
				// Nothing decoded: the file is missing or malformed.
				return err
			}
			if cfg.Mail.SMTPPass != "" {
				cfg.Mail.SMTPPass = "<redacted>"
			}
			fmt.Printf("# effective configuration from %s\n", configPath)
			if encErr := toml.NewEncoder(os.Stdout).Encode(cfg); encErr != nil {
				return encErr
			}
			// A validation failure still prints the config above; the
			// contradiction it names is what the reader came to see.
			return err
		},
	}
}
