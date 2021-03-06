package main

import (
	"os"
	"net"
	"fmt"
	"flag"
	"io/ioutil"
	"crypto/rsa"
	pso2net "aaronlindsay.com/go/pkg/pso2/net"
	"aaronlindsay.com/go/pkg/pso2/net/packets"
	"aaronlindsay.com/go/pkg/pso2/util"
	"github.com/juju/loggo"
)

var Logger loggo.Logger = loggo.GetLogger("pso2.cmd.pso2-net")

func usage() {
	fmt.Fprintln(os.Stderr, "usage: pso2-net [flags]")
	flag.PrintDefaults()
	os.Exit(2)
}

func ragequit(apath string, err error) {
	if err != nil {
		if apath != "" {
			Logger.Errorf("error with file %s", apath)
		}
		Logger.Errorf("%s", err)
		os.Exit(1)
	}
}

func findaddr() (addr string) {
	addr = "127.0.0.1"

	as, err := net.InterfaceAddrs()
	if err != nil {
		return
	}

	for _, a := range as {
		if ip, ok := a.(*net.IPNet); ok {
			ip := ip.IP.To4()
			if ip != nil && !ip.IsLoopback() && !ip.IsMulticast() {
				return ip.String()
			}
		}
	}

	return
}

type EndpointMap map[uint16]*pso2net.Proxy
func (e EndpointMap) EndpointAnnouncement(ip net.IP, port uint16) {
	if m, ok := e[port]; ok {
		m.ClientEndpoint = fmt.Sprintf("%s:%d", ip, port)
	} else {
		Logger.Warningf("Unknown endpoint announcement for %s:%d", ip, port)
	}
}

func main() {
	var flagPrivateKey, flagPublicKey, flagIP, flagBind, flagProxy, flagLog, flagDump, flagReplay string
	var keyPrivate *rsa.PrivateKey
	var keyPublic *rsa.PublicKey

	flag.Usage = usage
	flag.StringVar(&flagPrivateKey, "priv", "", "server private key")
	flag.StringVar(&flagPublicKey, "pub", "", "client public key")
	flag.StringVar(&flagLog, "log", "info", "log level (trace, debug, info, warning, error, critical)")
	flag.StringVar(&flagProxy, "proxy", "", "proxy server to connect to instead of PSO2")
	flag.StringVar(&flagBind, "bind", "", "interface address to bind on")
	flag.StringVar(&flagIP, "addr", findaddr(), "external IPv4 address")
	flag.StringVar(&flagDump, "dump", "", "dump packets to folder")
	flag.StringVar(&flagReplay, "replay", "", "replay packets from a dump")
	flag.Parse()

	ip := net.IPv4(127, 0, 0, 1)
	if flagIP != "" {
		ip = net.ParseIP(flagIP)
	}

	if flagLog != "" {
		lvl, ok := loggo.ParseLevel(flagLog)
		if ok {
			Logger.SetLogLevel(lvl)
		} else {
			Logger.Warningf("Invalid log level %s specified", flagLog)
		}
	}
	pso2net.Logger.SetLogLevel(Logger.LogLevel())

	if flagPrivateKey != "" {
		Logger.Infof("Loading private key")
		f, err := os.Open(flagPrivateKey)
		ragequit(flagPrivateKey, err)

		keyPrivate, err = pso2net.LoadPrivateKey(f)
		f.Close()

		ragequit(flagPrivateKey, err)
	}

	if flagPublicKey != "" {
		Logger.Infof("Loading public key")
		f, err := os.Open(flagPublicKey)
		ragequit(flagPublicKey, err)

		keyPublic, err = pso2net.LoadPublicKey(f)
		f.Close()
		ragequit(flagPublicKey, err)
	}

	if flagReplay != "" {
		Logger.Infof("Replaying packets from %s", flagReplay)

		f, err := os.Open(flagReplay)
		ragequit(flagReplay, err)

		c := pso2net.NewConnection(util.ReadWriter(f, ioutil.Discard))
		var r pso2net.PacketRoute
		err = c.RoutePackets(&r)
		ragequit(flagReplay, err)
	} else {
		Logger.Infof("Starting proxy servers on %s", ip)

		fallbackRoute := func(p *pso2net.Proxy) *pso2net.PacketRoute {
			r := &pso2net.PacketRoute{}
			r.RouteMask(0xffff, pso2net.RoutePriorityLow, pso2net.ProxyHandlerFallback(p))
			if flagDump != "" {
				r.RouteMask(0xffff, pso2net.RoutePriorityHigh, pso2net.HandlerIgnore(pso2net.HandlerDump(flagDump)))
			}
			return r
		}

		newProxy := func(host string, port uint16) *pso2net.Proxy {
			return pso2net.NewProxy(fmt.Sprintf("%s:%d", flagBind, port), fmt.Sprintf("%s:%d", host, port))
		}

		startProxy := func(p *pso2net.Proxy, s *pso2net.PacketRoute, c *pso2net.PacketRoute) {
			l, err := p.Listen()
			ragequit(p.String(), err)

			go p.Start(l, s, c)
		}

		hostname := func(ship int) string {
			if flagProxy != "" {
				return flagProxy
			}

			return packets.ShipHostnames[ship]
		}

		endpoints := make(EndpointMap)

		for i := 0; i < packets.ShipCount; i++ {
			blockPort := uint16(12000 + (100 * i))
			shipPort := uint16(blockPort + 99)

			// Set up ship proxy, rewrites IPs
			proxy := newProxy(hostname(i), shipPort)
			route := &pso2net.PacketRoute{}
			route.Route(packets.TypeShip, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerShip(proxy, endpoints, ip))
			route.RouteMask(0xffff, pso2net.RoutePriorityLow, pso2net.ProxyHandlerFallback(proxy))
			if flagDump != "" {
				route.RouteMask(0xffff, pso2net.RoutePriorityHigh, pso2net.HandlerIgnore(pso2net.HandlerDump(flagDump)))
			}
			endpoints[shipPort] = proxy
			startProxy(proxy, fallbackRoute(proxy), route)

			// Set up block proxy, rewrites IPs
			proxy = newProxy(hostname(i), blockPort)
			route = &pso2net.PacketRoute{}
			route.Route(packets.TypeBlock, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerBlock(proxy, endpoints, ip))
			route.RouteMask(0xffff, pso2net.RoutePriorityLow, pso2net.ProxyHandlerFallback(proxy))
			if flagDump != "" {
				route.RouteMask(0xffff, pso2net.RoutePriorityHigh, pso2net.HandlerIgnore(pso2net.HandlerDump(flagDump)))
			}
			endpoints[blockPort] = proxy
			startProxy(proxy, fallbackRoute(proxy), route)

			for b := uint16(1); b < 99; b++ {
				port := blockPort + b
				proxy = newProxy(hostname(i), port)

				// Set up client route (messages from the PSO2 server)
				route = &pso2net.PacketRoute{}
				route.Route(packets.TypeRoom, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerRoom(proxy, endpoints, ip))
				route.Route(packets.TypeRoomTeam, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerRoom(proxy, endpoints, ip))
				route.Route(packets.TypeBlockResponse, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerBlockResponse(proxy, endpoints, ip))
				route.Route(packets.TypeBlocks, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerBlocks(proxy, endpoints, ip))
				route.Route(packets.TypeBlocks2, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerBlocks(proxy, endpoints, ip))
				route.RouteMask(0xffff, pso2net.RoutePriorityLow, pso2net.ProxyHandlerFallback(proxy))
				if flagDump != "" {
					route.RouteMask(0xffff, pso2net.RoutePriorityHigh, pso2net.HandlerIgnore(pso2net.HandlerDump(flagDump)))
				}

				// Set up server route (messages from the client)
				sroute := &pso2net.PacketRoute{}
				sroute.Route(packets.TypeCipher, pso2net.RoutePriorityHigh, pso2net.HandlerIgnore(pso2net.HandlerCipher(keyPrivate)))
				sroute.Route(packets.TypeCipher, pso2net.RoutePriorityNormal, pso2net.ProxyHandlerCipher(proxy, keyPrivate, keyPublic))
				sroute.RouteMask(0xffff, pso2net.RoutePriorityLow, pso2net.ProxyHandlerFallback(proxy))
				if flagDump != "" {
					sroute.RouteMask(0xffff, pso2net.RoutePriorityHigh, pso2net.HandlerIgnore(pso2net.HandlerDump(flagDump)))
				}

				endpoints[port] = proxy
				startProxy(proxy, sroute, route)
			}
		}
	}

	// Stop foreverz
	<-make(chan int)
}
