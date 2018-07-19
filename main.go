package main

import (
	"crypto/subtle"
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

	if *help {
		flag.Usage()

		return
	}

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
		log.Fatalf("Failed to listen on %s (%v)", *addr, err)
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
					return nil, fmt.Errorf("internal server error: %v", err)
				}
			} else {
				rows, err = adapters.db.Query("SELECT id, cid, defaultShell, uid FROM containers WHERE name=?", c.User())

				if err != nil {
					return nil, fmt.Errorf("internal server error: %v", err)
				}
			}

			if !rows.Next() {
				rows.Close()

				return nil, fmt.Errorf("not found")
			}

			var nullableDefaultShell sql.NullString
			if err := rows.Scan(&id, &cid, &nullableDefaultShell, &uid); err != nil {
				rows.Close()
				return nil, fmt.Errorf("internal server error: %v", err)
			}
			rows.Close()

			defaultShell = nullableDefaultShell.String

			var keys []string
			if err := adapters.db.Select(&keys, "SELECT `key` FROM authorizedKeys WHERE uid=?", uid); err != nil {
				return nil, fmt.Errorf("DB error: %v", err)
			}

			perm := &ssh.Permissions{
				CriticalOptions: map[string]string{
					permCIDKey:   cid,
					permIDKey:    strconv.Itoa(id),
					permUIDKey:   strconv.Itoa(uid),
					permShellKey: defaultShell,
				},
			}

			for i := range keys {
				key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(keys[i]))

				if err != nil {
					continue
				}

				a := key.Marshal()
				b := pubKey.Marshal()
				if len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1 {
					return perm, nil
				}
			}

			return nil, fmt.Errorf("Permission denied(public key)")
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
			log.Printf("Failed to accept incoming connection (%v)", err)
			continue
		}

		cw := connWorker{
			adapters: &adapters,
		}

		go cw.run(tcpConn, config)
	}
}

type authorizedKey struct {
	Key   string
	Label string
}
