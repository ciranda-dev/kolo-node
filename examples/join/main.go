// Command join starts a kolo-dht client and bootstraps it from the kolo-node servers given as arguments.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "UDP address of this client; \":0\" to reach servers on other hosts")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("usage: join [-listen ADDR] IP:PORT...")
	}
	var servers []netip.AddrPort
	for _, a := range flag.Args() {
		ap, err := netip.ParseAddrPort(a)
		if err != nil {
			log.Fatal(err)
		}
		servers = append(servers, ap)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	id, err := koloid.Generate()
	if err != nil {
		log.Fatal(err)
	}
	n, err := kolodht.New(ctx, *listen, id, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer n.Close()
	if err := n.Bootstrap(ctx, servers); err != nil {
		log.Fatal(err)
	}
	fmt.Println("joined:", n.Stats().Servers, "servers in the routing table")
}
