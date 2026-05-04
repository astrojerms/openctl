package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openctl/openctl/pkg/k3s/agent"
)

func main() {
	listen := flag.String("listen", ":9443", "HTTPS listen address")
	cert := flag.String("cert", "/etc/openctl-k3s-agent/server.pem", "Server certificate (PEM)")
	key := flag.String("key", "/etc/openctl-k3s-agent/server.key", "Server private key (PEM)")
	ca := flag.String("ca", "/etc/openctl-k3s-agent/ca.pem", "CA certificate for client authentication (PEM)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(agent.Version)
		return
	}

	s, err := agent.New(agent.Options{
		Listen:   *listen,
		CertFile: *cert,
		KeyFile:  *key,
		CAFile:   *ca,
	})
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("openctl-k3s-agent %s listening on %s", agent.Version, *listen)
		errCh <- s.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		log.Fatalf("server: %v", err)
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.Shutdown(ctx)
		cancel()
		if err != nil {
			log.Fatalf("shutdown: %v", err)
		}
	}
}
