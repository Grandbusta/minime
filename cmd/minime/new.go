package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Grandbusta/minime/internal/channel/telegram"
	"github.com/Grandbusta/minime/internal/setup"
	"github.com/Grandbusta/minime/internal/store"
)

// newPerson returns whether to run the person now. What it wrote to .env is
// set in this process's environment too.
func newPerson(ctx context.Context) (bool, error) {
	s := &setup.Setup{Ask: setup.Terminal{}, Out: os.Stdout, Lives: "lives", Self: "self", Env: ".env", Example: ".env.example",
		BotName: telegram.BotName, FirstMessage: telegram.FirstMessage, Database: databaseWhose}
	if _, err := exec.LookPath("docker"); err == nil {
		s.Postgres = dockerPostgres
	}
	res, err := s.Run(ctx)
	if setup.Stopped(err) {
		fmt.Println("\nStopped; nothing was written.")
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for k, v := range res.Env {
		os.Setenv(k, v)
	}
	if !res.Start {
		fmt.Printf("\nStart %s whenever you like with: go run ./cmd/minime\n", res.Name)
	}
	return res.Start, nil
}

func databaseWhose(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st, err := store.Open(ctx, url)
	if err != nil {
		return "", err
	}
	defer st.Close()
	whose, _, err := st.Whose(ctx)
	return whose, err
}

func dockerPostgres(ctx context.Context, name string) (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	b := make([]byte, 12)
	rand.Read(b)
	password := hex.EncodeToString(b)
	container := "minime-" + name + "-db"
	out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", container, "--restart", "unless-stopped",
		"-e", "POSTGRES_PASSWORD="+password, "-e", "POSTGRES_DB=minime", "-v", container+":/var/lib/postgresql/data",
		"-p", fmt.Sprintf("127.0.0.1:%d:5432", port), "postgres:16-alpine").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run: %s", strings.TrimSpace(string(out)))
	}
	for i := 0; i < 60; i++ {
		if exec.CommandContext(ctx, "docker", "exec", container, "pg_isready", "-U", "postgres", "-d", "minime").Run() == nil {
			time.Sleep(time.Second) // it restarts once after its first setup
			if exec.CommandContext(ctx, "docker", "exec", container, "pg_isready", "-U", "postgres", "-d", "minime").Run() == nil {
				return fmt.Sprintf("postgres://postgres:%s@127.0.0.1:%d/minime?sslmode=disable", password, port), nil
			}
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("%s didn't come up", container)
}
