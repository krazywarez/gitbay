package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
)

// auditVerifyCmd recomputes the audit log's hash chain. A break names
// the first row that does not match. Rows removed from the end of the
// log, and rows written after that under the reused ids, leave no
// break: the last id and hash printed here are what an operator
// compares with the daemon's journal copy to see either.
func auditVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "check the audit log's hash chain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			res, err := st.VerifyAuditChain()
			if err != nil {
				return err
			}
			fmt.Printf("rows %d\nunchained %d\n", res.Rows, res.Unchained)
			if res.BrokenAt != 0 {
				return fmt.Errorf("chain broken at row %d: %s", res.BrokenAt, res.Reason)
			}
			// Every row unchained is what dropping and re-adding the hash
			// columns leaves. It is also an upgrade from before migration
			// 0064 with no row written since; the first new row clears it.
			if res.Rows > 0 && res.Unchained == res.Rows {
				return fmt.Errorf("no row carries a hash: the chain columns were cleared, or no audit row has been written since the upgrade")
			}
			if res.Last == 0 {
				fmt.Println("no rows yet")
				return nil
			}
			fmt.Printf("first %d\nlast %d\nlast hash %s\nchain intact\n", res.First, res.Last, res.LastHash)
			return nil
		},
	}
}
