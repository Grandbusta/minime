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

	"github.com/Grandbusta/minime"
	"github.com/Grandbusta/minime/internal/channel/telegram"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/person"
	"github.com/Grandbusta/minime/internal/secret"
	"github.com/Grandbusta/minime/internal/setup"
	"github.com/Grandbusta/minime/internal/store"
)

// newPerson returns whether to run the person now. This process's
// environment then has the new .env, and nothing from the one before.
func newPerson(ctx context.Context) (bool, error) {
	s := &setup.Setup{Ask: setup.Terminal{}, Out: os.Stdout, Files: minime.Files, Lives: "lives", Example: ".env.example", Env: ".env",
		BotName: telegram.BotName, FirstMessage: telegram.FirstMessage, Database: databaseWhose, Save: saveNew}
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
	for _, k := range res.Dropped {
		os.Unsetenv(k)
	}
	for k, v := range res.Env {
		os.Setenv(k, v)
	}
	if !res.Start {
		fmt.Printf("\nStart %s whenever you like with: minime\n", res.Name)
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

func saveNew(ctx context.Context, url, key string, files map[string][]byte, secrets map[string]string) error {
	k, err := secret.ParseKey(key)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer st.Close()
	name, ok, err := person.Start(ctx, st, k, files, secrets, nil, memory.SelfNew, time.Now().UTC())
	if err == nil && !ok {
		err = fmt.Errorf("someone already lives in that database; %s needs their own", name)
	}
	return err
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
