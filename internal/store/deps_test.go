package store

import "testing"

// The bot's name was not reserved before migration 0028, so an instance
// upgrading from v1.0.x may already have an owner holding it. The migration
// has to survive that: a daemon that will not start is worse than a
// dependency check that cannot open an issue.
func TestBotNameCollisionDoesNotBlockMigration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		occupy func(*Store) error
	}{
		{"user", func(s *Store) error {
			_, err := s.DB.Exec("INSERT INTO users (username, is_admin) VALUES (?, 0)", BotUsername)
			return err
		}},
		{"org", func(s *Store) error {
			_, err := s.DB.Exec("INSERT INTO orgs (name) VALUES (?)", BotUsername)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t)
			if err := s.MigrateTo(27); err != nil {
				t.Fatal(err)
			}
			if err := tc.occupy(s); err != nil {
				t.Fatal(err)
			}
			if err := s.MigrateTo(28); err != nil {
				t.Fatalf("migration 0028 failed with %s %q present: %v", tc.name, BotUsername, err)
			}
			var n int
			if err := s.DB.QueryRow("SELECT count(*) FROM users WHERE username = ?", BotUsername).Scan(&n); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.name == "user" {
				want = 1 // the pre-existing account, not a second one
			}
			if n != want {
				t.Errorf("users named %q = %d, want %d", BotUsername, n, want)
			}
		})
	}
}

// On a clean instance the account is created, which is what every other
// dependency test assumes.
func TestBotAccountCreatedWhenNameIsFree(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByUsername(BotUsername); err != nil {
		t.Fatalf("no %s account after a clean migration: %v", BotUsername, err)
	}
}
