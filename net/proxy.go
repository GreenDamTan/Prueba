package net

import (
	"net"
	"fmt"
	"sync"
	"crypto/rsa"
	"aaronlindsay.com/go/pkg/pso2/net/packets"
)

type Proxy struct {
	serverEndpoint, clientEndpoint string

	connections map[*Connection]*Connection
	connectionsLock sync.Mutex
}

func NewProxy(serverEndpoint, clientEndpoint string) *Proxy {
	return &Proxy{serverEndpoint, clientEndpoint, make(map[*Connection]*Connection), sync.Mutex{}}
}

func (p *Proxy) Listen() (net.Listener, error) {
	return net.Listen("tcp4", p.serverEndpoint)
}

func (p *Proxy) Start(l net.Listener, serverRoute *PacketRoute, clientRoute *PacketRoute) error {
	for {
		conn, err := l.Accept()

		if err != nil {
			Logger.Errorf("%s %s listener error. %s", p, l, err)
			return err
		}

		go func() {
			Logger.Infof("%s new connection from %s", p, conn.RemoteAddr())
			c := NewConnection(conn)
			client, err := p.Connect(c)
			if err != nil {
				Logger.Errorf("%s %s connection failed. %s", p, c, err)
				c.Close()
			} else {
				p.Proxy(c, serverRoute, client, clientRoute)
				c.Close()
				client.Close()
			}
		}()
	}
}

func (p *Proxy) Connect(c *Connection) (*Connection, error) {
	clientConn, err := net.Dial("tcp4", p.clientEndpoint)

	if err != nil {
		return nil, err
	}

	return NewConnection(clientConn), nil
}

func (p *Proxy) Proxy(server *Connection, serverRoute *PacketRoute, client *Connection, clientRoute *PacketRoute) error {
	Logger.Infof("%s Proxying connection %s", p, server)

	ch := make(chan error)

	k := func(c *Connection, r *PacketRoute) {
		err := c.RoutePackets(r)
		ch <- err
	}

	p.connectionsLock.Lock()
	p.connections[server] = client
	p.connections[client] = server
	p.connectionsLock.Unlock()

	go k(server, serverRoute)
	go k(client, clientRoute)

	var err error
	for i := 0; i < 2; i++ {
		e := <-ch
		if err == nil {
			err = e
		}

		server.Close()
		client.Close()
	}

	p.connectionsLock.Lock()
	delete(p.connections, server)
	delete(p.connections, client)
	p.connectionsLock.Unlock()

	Logger.Infof("%s Proxy %s closed. %s", p, server, err)

	return err
}

func (p *Proxy) Destination(c *Connection) (d *Connection) {
	p.connectionsLock.Lock()
	d = p.connections[c]
	p.connectionsLock.Unlock()
	return
}

func (p *Proxy) String() string {
	return fmt.Sprintf("[pso2/net/proxy: %s -> %s]", p.serverEndpoint, p.clientEndpoint)
}


func ProxyHandlerShip(p *Proxy, ip net.IP) PacketHandler {
	return proxyHandlerShip{p, ip}
}

type proxyHandlerShip struct {
	proxy *Proxy
	addr net.IP
}

func (h proxyHandlerShip) HandlePacket(c *Connection, p *packets.Packet) (bool, error) {
	Logger.Debugf("%s %s ship packet, rewriting addresses", h.proxy, c)

	s, err := packets.ParseShip(p)
	if err != nil {
		return false, err
	}

	for i := range s.Entries {
		e := &s.Entries[i]

		if h.addr != nil {
			e.SetAddress(h.addr)
		}
	}

	p, err = s.Packet()

	if err != nil {
		return false, err
	}

	return true, h.proxy.Destination(c).WritePacket(p)
}

func ProxyHandlerBlock(p *Proxy, ip net.IP) PacketHandler {
	return proxyHandlerBlock{p, ip}
}

type proxyHandlerBlock struct {
	proxy *Proxy
	addr net.IP
}

func (h proxyHandlerBlock) HandlePacket(c *Connection, p *packets.Packet) (bool, error) {
	Logger.Debugf("%s %s block packet, rewriting addresses", h.proxy, c)

	b, err := packets.ParseBlock(p)
	if err != nil {
		return false, err
	}

	b.SetAddress(h.addr)

	p, err = b.Packet()

	if err != nil {
		return false, err
	}

	return true, h.proxy.Destination(c).WritePacket(p)
}

func ProxyHandlerCipher(p *Proxy, privateKey *rsa.PrivateKey, publicKey *rsa.PublicKey) PacketHandler {
	return proxyHandlerCipher{p, privateKey, publicKey}
}

type proxyHandlerCipher struct {
	proxy *Proxy
	privateKey *rsa.PrivateKey
	publicKey *rsa.PublicKey
}

func (h proxyHandlerCipher) HandlePacket(c *Connection, p *packets.Packet) (bool, error) {
	Logger.Debugf("%s %s cipher packet, re-encrypting", h.proxy, c)

	b, err := packets.ParseCipher(p)
	if err != nil {
		return false, err
	}

	key, err := b.Key(h.privateKey)
	if err != nil {
		return false, err
	}

	rc4key, err := packets.CipherRC4Key(key)
	if err != nil {
		return false, err
	}

	b.SetKey(key, h.publicKey)

	p, err = b.Packet()
	if err != nil {
		return false, err
	}

	dest := h.proxy.Destination(c)
	err = dest.WritePacket(p)
	if err == nil {
		err = dest.SetCipher(rc4key)
	}

	return true, err
}

func ProxyHandlerFallback(p *Proxy) PacketHandler {
	return proxyHandlerFallback{p}
}

type proxyHandlerFallback struct {
	proxy *Proxy
}

func (h proxyHandlerFallback) HandlePacket(c *Connection, p *packets.Packet) (bool, error) {
	Logger.Tracef("%s %s unknown packet %s, forwarding", h.proxy, c, p)

	return true, h.proxy.Destination(c).WritePacket(p)
}