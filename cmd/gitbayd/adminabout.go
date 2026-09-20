package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// adminMigrateProfileAboutCmd drains profile_about_backfill: each owner's
// parked about text becomes profile/README.* in <owner>/.gitbay. Idempotent
// — an owner who already has the file keeps it and loses the row.
func adminMigrateProfileAboutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate-profile-about",
		Short: "write parked profile about text into each owner's .gitbay repository",
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
			rows, err := st.PendingAboutBackfill()
			if err != nil {
				return err
			}
			n := 0
			for _, row := range rows {
				written, err := writeAbout(cfg, st, row)
				if err != nil {
					return fmt.Errorf("%s: %w", row.OwnerName, err)
				}
				if err := st.ClearAboutBackfill(row.OwnerKind, row.OwnerID); err != nil {
					return err
				}
				if written {
					n++
				}
			}
			fmt.Printf("wrote %d profile about file(s)\n", n)
			return nil
		},
	}
}

// writeAbout creates <owner>/.gitbay if it does not exist and commits the
// about at the recorded format. It reports whether it wrote anything: an
// owner who already has the file is left alone.
func writeAbout(cfg config.Config, st *store.Store, row store.AboutRow) (bool, error) {
	path := row.OwnerName + "/" + control.ProfileRepoName
	repo, err := st.RepoByPath(path)
	if err != nil {
		// Public, because the about it carries was public where it was.
		id, cerr := st.CreateRepo(row.OwnerKind, row.OwnerID, control.ProfileRepoName, "public")
		if cerr != nil {
			return false, cerr
		}
		dir := control.RepoDir(cfg.Server.Root, row.OwnerName, control.ProfileRepoName)
		if ierr := gitutil.InitBare(dir, "main", control.HooksDir(cfg.Server.Root)); ierr != nil {
			st.DeleteRepo(id)
			return false, ierr
		}
		if repo, err = st.RepoByPath(path); err != nil {
			return false, err
		}
	}
	dir := control.RepoDir(cfg.Server.Root, repo.OwnerName, repo.Name)
	ext := ".md"
	if row.Format == "org" {
		ext = ".org"
	}
	file := control.AboutBase + ext
	if _, err := gitutil.ReadBlob(dir, repo.DefaultBranch, file, 1); err == nil {
		return false, nil // already there
	}
	// A commit carries an identity. An owner without a verified address,
	// and every org, gets the noreply form rather than no commit.
	email := row.OwnerName + "@users.noreply." + cfg.SiteHost()
	if row.OwnerKind == "user" {
		if addr, _ := st.PrimaryVerifiedEmail(row.OwnerID); addr != "" {
			email = addr
		}
	}
	if _, err := gitutil.CommitFileChange(dir, repo.DefaultBranch, file,
		[]byte(row.About), row.OwnerName, email,
		"move profile about out of the database"); err != nil {
		return false, err
	}
	return true, nil
}
