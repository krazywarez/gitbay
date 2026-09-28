package gitd

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/store"
)

func TestBusyAnswersERR(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := st.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRepoSettings(repoID, func(rs *store.RepoSettings) { rs.GitDaemon = true }); err != nil {
		t.Fatal(err)
	}
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, _ := packs.Acquire(nil, "ip:elsewhere")
	defer hold()

	s := New(config.Config{Server: config.Server{Root: t.TempDir()}}, st, packs)
	client, server := net.Pipe()
	defer client.Close()
	go s.handle(server)
	req := "git-upload-pack /alice/app.git\x00host=x\x00"
	fmt.Fprintf(client, "%04x%s", len(req)+4, req)
	client.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := readPktLine(client)
	if err != nil || !strings.HasPrefix(line, "ERR ") || !strings.Contains(line, "busy") {
		t.Fatalf("got %q, %v", line, err)
	}
}
