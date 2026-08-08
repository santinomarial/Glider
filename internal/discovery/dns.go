// Package discovery serves service endpoint addresses through cluster DNS.
package discovery

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/santinomarial/glider/internal/api"
)

type ServiceLister interface {
	ListServices(context.Context) ([]api.Service, error)
}
type DNS struct {
	store ServiceLister
	ttl   uint32
}

func NewDNS(store ServiceLister) (*DNS, error) {
	if store == nil {
		return nil, errors.New("service store is required")
	}
	return &DNS{store: store, ttl: 10}, nil
}

func (d *DNS) Lookup(ctx context.Context, name string) ([]net.IP, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if !strings.HasSuffix(name, ".glider") {
		return nil, nil
	}
	id := strings.TrimSuffix(name, ".glider")
	services, err := d.store.ListServices(ctx)
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, service := range services {
		if strings.ToLower(service.Metadata.Name) != id && strings.ToLower(service.Metadata.ID) != id {
			continue
		}
		for _, ep := range service.Status.Endpoints {
			if ip := net.ParseIP(ep.Address).To4(); ip != nil {
				out = append(out, ip)
			}
		}
		break
	}
	return out, nil
}

// ServeUDP runs a bounded authoritative A-record server until ctx is canceled.
func (d *DNS) ServeUDP(ctx context.Context, address string) error {
	conn, err := net.ListenPacket("udp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	go func() { <-ctx.Done(); _ = conn.Close() }()
	buf := make([]byte, 1232)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		response := d.response(ctx, append([]byte(nil), buf[:n]...))
		if len(response) > 0 {
			_, _ = conn.WriteTo(response, peer)
		}
	}
}
func (d *DNS) response(ctx context.Context, q []byte) []byte {
	if len(q) < 12 || binary.BigEndian.Uint16(q[4:6]) != 1 {
		return nil
	}
	off := 12
	var labels []string
	for off < len(q) {
		size := int(q[off])
		off++
		if size == 0 {
			break
		}
		if size > 63 || off+size > len(q) {
			return nil
		}
		labels = append(labels, string(q[off:off+size]))
		off += size
	}
	if off+4 > len(q) {
		return nil
	}
	qend := off + 4
	qtype := binary.BigEndian.Uint16(q[off : off+2])
	ips, err := d.Lookup(ctx, strings.Join(labels, "."))
	if err != nil {
		return nil
	}
	flags := uint16(0x8400)
	if len(ips) == 0 {
		flags |= 3
	}
	answerCount := len(ips)
	if qtype != 1 {
		answerCount = 0
	}
	r := make([]byte, 12, qend+answerCount*16)
	copy(r[:2], q[:2])
	binary.BigEndian.PutUint16(r[2:4], flags)
	binary.BigEndian.PutUint16(r[4:6], 1)
	binary.BigEndian.PutUint16(r[6:8], uint16(answerCount))
	r = append(r, q[12:qend]...)
	for _, ip := range ips {
		if qtype != 1 {
			break
		}
		r = append(r, 0xc0, 0x0c, 0, 1, 0, 1)
		ttl := make([]byte, 4)
		binary.BigEndian.PutUint32(ttl, d.ttl)
		r = append(r, ttl...)
		r = append(r, 0, 4)
		r = append(r, ip.To4()...)
	}
	return r
}

func WaitReady(ctx context.Context, address string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		c, err := net.DialTimeout("udp", address, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
