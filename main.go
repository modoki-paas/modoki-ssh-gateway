package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/cs3238-tsuzu/modoki/consul_traefik"
	"github.com/docker/docker/client"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/ssh"
)

var (
	sqlDriver        = flag.String("driver", "mysql", "SQL Driver")
	sshPrivateKey    = flag.String("key", "./id_rsa", "Path to RSA private key file")
	docker           = flag.String("docker", "unix:///var/run/docker.sock", "Docker path")
	dockerAPIVersion = flag.String("docker-api", "v1.37", "Docker API version")
	sqlHost          = flag.String("db", "root:password@tcp(localhost:3306)/modoki?charset=utf8mb4&parseTime=True", "SQL")
	consulHost       = flag.String("consul", "localhost:8500", "Consul(KV)")
	addr             = flag.String("addr", ":2022", "Address for SSH to listen on")
	help             = flag.Bool("help", false, "Show this")
)

type adaptersType struct {
	db     *sqlx.DB
	consul *consulTraefik.Client
	docker *client.Client
}

func main() {
	flag.Parse()

	var adapters adaptersType
	var err error

	adapters.db, err = sqlx.Connect(*sqlDriver, *sqlHost)

	if err != nil {
		log.Fatal("error: Connecting to SQL server error: ", err)
	}

	adapters.consul, err = consulTraefik.NewClient("traefik", *consulHost)

	if err != nil {
		log.Fatal("error: Connecting to Zookeeper server error", err)
	}

	adapters.docker, err = client.NewClient(*docker, *dockerAPIVersion, nil, nil)

	if err != nil {
		log.Fatal("Docker client initialization error", err)
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s (%s)", *addr, err)
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(c ssh.ConnMetadata, pubKey ssh.PublicKey) (*ssh.Permissions, error) {
			var id, uid int
			var cid, defaultShell string
			var err error

			var rows *sql.Rows
			if strings.HasPrefix(c.User(), "id.") {
				id, err = strconv.Atoi(strings.TrimPrefix(c.User(), "id."))

				if err != nil {
					return nil, errors.New("id must be integer")
				}

				rows, err = adapters.db.Query("SELECT id, cid, defaultShell, uid FROM containers WHERE id=?", id)

				if err != nil {
					return nil, fmt.Errorf("internal server error: %s", err)
				}
			} else {
				rows, err = adapters.db.Query("SELECT id, cid, defaultShell, uid FROM containers WHERE cid=?", c.User())

				if err != nil {
					return nil, fmt.Errorf("internal server error: %s", err)
				}
				defer rows.Close()
			}
			defer rows.Close()

			rows.Next()
			if err := rows.Scan(&id, &cid, &defaultShell, &uid); err != nil {
				return nil, fmt.Errorf("internal server error: %s", err)
			}

			perm := &ssh.Permissions{
				CriticalOptions: map[string]string{
					permCIDKey:   cid,
					permIDKey:    strconv.Itoa(id),
					permUIDKey:   strconv.Itoa(uid),
					permShellKey: defaultShell,
				},
			}

			return perm, nil
		},
	}

	privateBytes, err := ioutil.ReadFile(*sshPrivateKey)
	if err != nil {
		log.Fatalf("Failed to load private key (%s)", *sshPrivateKey)
	}

	private, err := ssh.ParsePrivateKey(privateBytes)
	if err != nil {
		log.Fatal("Failed to parse private key")
	}

	config.AddHostKey(private)

	for {
		tcpConn, err := listener.Accept()
		if err != nil {
			log.Printf("Failed to accept incoming connection (%s)", err)
			continue
		}

		cw := connWorker{
			adapters: &adapters,
		}

		go cw.run(tcpConn, config)
	}
}
