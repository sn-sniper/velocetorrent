package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MaxUDPKernelBuffer is the 16 MiB target socket buffer size matching /etc/sysctl.d/99-velocetorrent.conf.
	MaxUDPKernelBuffer = 16 * 1024 * 1024
	// FallbackUDPBuffer is the 4 MiB fallback when the OS kernel limit is not yet raised.
	FallbackUDPBuffer = 4 * 1024 * 1024
)

// TuneUDPSocketBuffers configures a *net.UDPConn with 16 MiB receive/send buffers (falling back to 4 MiB).
func TuneUDPSocketBuffers(conn *net.UDPConn) {
	if conn == nil {
		return
	}
	if err := conn.SetReadBuffer(MaxUDPKernelBuffer); err != nil {
		_ = conn.SetReadBuffer(FallbackUDPBuffer)
	}
	if err := conn.SetWriteBuffer(MaxUDPKernelBuffer); err != nil {
		_ = conn.SetWriteBuffer(FallbackUDPBuffer)
	}
}

// NATStatus Snapshot reports router port-forwarding state in EngineTelemetrySnapshot.
type NATStatus struct {
	Mapped       bool   `json:"mapped"`
	Protocol     string `json:"protocol"` // "UPnP-IGD", "NAT-PMP", or "NONE"
	ExternalIP   string `json:"externalIp,omitempty"`
	ExternalPort int    `json:"externalPort,omitempty"`
	InternalPort int    `json:"internalPort"`
}

// NATMapper manages automatic UPnP IGD and NAT-PMP router port forwarding and periodic lease renewal.
type NATMapper struct {
	mu           sync.RWMutex
	internalPort int
	externalPort int
	externalIP   string
	protocol     string
	controlURL   string
	serviceType  string
	gatewayIP    net.IP
	mapped       atomic.Bool
	closed       atomic.Bool
	closeCh      chan struct{}
	wg           sync.WaitGroup
}

// NewNATMapper creates and starts a background UPnP / NAT-PMP port mapper for internalPort.
func NewNATMapper(internalPort int) *NATMapper {
	m := &NATMapper{
		internalPort: internalPort,
		externalPort: internalPort,
		protocol:     "NONE",
		closeCh:      make(chan struct{}),
	}
	m.wg.Add(1)
	go m.lifecycleLoop()
	return m
}

func (m *NATMapper) lifecycleLoop() {
	defer m.wg.Done()

	m.attemptMapping()

	ticker := time.NewTicker(20 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.closeCh:
			return
		case <-ticker.C:
			m.attemptMapping()
		}
	}
}

func (m *NATMapper) attemptMapping() {
	if m.closed.Load() || m.internalPort <= 0 {
		return
	}

	// 1. Try UPnP IGD SSDP Discovery + SOAP AddPortMapping
	if extIP, ctrlURL, svcType, err := m.tryUPnPMapping(m.internalPort, 3600); err == nil {
		m.mu.Lock()
		m.externalIP = extIP
		m.controlURL = ctrlURL
		m.serviceType = svcType
		m.protocol = "UPnP-IGD"
		m.externalPort = m.internalPort
		m.mu.Unlock()
		m.mapped.Store(true)
		return
	}

	// 2. Fallback: Try NAT-PMP (RFC 6886) on default candidate gateways
	if extIP, extPort, gw, err := m.tryNATPMPMapping(m.internalPort, 3600); err == nil {
		m.mu.Lock()
		m.externalIP = extIP
		m.externalPort = extPort
		m.gatewayIP = gw
		m.protocol = "NAT-PMP"
		m.mu.Unlock()
		m.mapped.Store(true)
		return
	}
}

// Snapshot returns the current NATStatus for telemetry reporting.
func (m *NATMapper) Snapshot() NATStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return NATStatus{
		Mapped:       m.mapped.Load(),
		Protocol:     m.protocol,
		ExternalIP:   m.externalIP,
		ExternalPort: m.externalPort,
		InternalPort: m.internalPort,
	}
}

// Close removes any active UPnP or NAT-PMP port mapping on clean daemon shutdown.
func (m *NATMapper) Close() {
	if !m.closed.CompareAndSwap(false, true) {
		return
	}
	close(m.closeCh)
	m.wg.Wait()

	if !m.mapped.Load() {
		return
	}

	m.mu.RLock()
	proto := m.protocol
	ctrlURL := m.controlURL
	svcType := m.serviceType
	gw := m.gatewayIP
	port := m.internalPort
	m.mu.RUnlock()

	switch proto {
	case "UPnP-IGD":
		_ = sendUPnPDeletePortMapping(ctrlURL, svcType, port, "TCP")
		_ = sendUPnPDeletePortMapping(ctrlURL, svcType, port, "UDP")
	case "NAT-PMP":
		if gw != nil {
			_, _ = sendNATPMPPortMap(gw, 2, port, 0, 0) // lifetime 0 = unmap TCP
			_, _ = sendNATPMPPortMap(gw, 1, port, 0, 0) // lifetime 0 = unmap UDP
		}
	}
	m.mapped.Store(false)
}

func (m *NATMapper) tryUPnPMapping(port int, leaseSeconds int) (extIP, controlURL, serviceType string, err error) {
	ssdpAddr, err := net.ResolveUDPAddr("udp4", "239.255.255.250:1900")
	if err != nil {
		return "", "", "", err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return "", "", "", err
	}
	defer conn.Close()
	TuneUDPSocketBuffers(conn)
	_ = conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))

	targets := []string{
		"urn:schemas-upnp-org:service:WANIPConnection:1",
		"urn:schemas-upnp-org:service:WANPPPConnection:1",
	}

	for _, st := range targets {
		req := strings.Join([]string{
			"M-SEARCH * HTTP/1.1",
			"HOST: 239.255.255.250:1900",
			"MAN: \"ssdp:discover\"",
			"MX: 1",
			"ST: " + st,
			"",
			"",
		}, "\r\n")
		_, _ = conn.WriteToUDP([]byte(req), ssdpAddr)
	}

	buf := make([]byte, 4096)
	var locationURL string
	for {
		n, _, readErr := conn.ReadFromUDP(buf)
		if readErr != nil {
			break
		}
		respText := string(buf[:n])
		for _, line := range strings.Split(respText, "\r\n") {
			if strings.HasPrefix(strings.ToLower(line), "location:") {
				locationURL = strings.TrimSpace(line[len("location:"):])
				break
			}
		}
		if locationURL != "" {
			break
		}
	}

	if locationURL == "" {
		return "", "", "", errors.New("no UPnP IGD gateway responded to SSDP M-SEARCH")
	}

	ctrlURL, svcType, err := parseUPnPControlURL(locationURL)
	if err != nil {
		return "", "", "", err
	}

	localIP, err := discoverLocalOutboundIP(ctrlURL)
	if err != nil {
		return "", "", "", err
	}

	if err := sendUPnPAddPortMapping(ctrlURL, svcType, port, localIP, leaseSeconds, "TCP"); err != nil {
		return "", "", "", err
	}
	_ = sendUPnPAddPortMapping(ctrlURL, svcType, port, localIP, leaseSeconds, "UDP")

	extIP, _ = queryUPnPExternalIP(ctrlURL, svcType)
	return extIP, ctrlURL, svcType, nil
}

var (
	reServiceBlock = regexp.MustCompile(`(?s)<service>(.*?)</service>`)
	reServiceType  = regexp.MustCompile(`(?s)<serviceType>\s*(.*?)\s*</serviceType>`)
	reControlURL   = regexp.MustCompile(`(?s)<controlURL>\s*(.*?)\s*</controlURL>`)
	reExtIP        = regexp.MustCompile(`(?s)<NewExternalIPAddress>\s*(.*?)\s*</NewExternalIPAddress>`)
)

func parseUPnPControlURL(descURL string) (controlURL string, serviceType string, err error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(descURL)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	rawXML, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", "", err
	}

	blocks := reServiceBlock.FindAllStringSubmatch(string(rawXML), -1)
	for _, blk := range blocks {
		body := blk[1]
		stMatch := reServiceType.FindStringSubmatch(body)
		ctrlMatch := reControlURL.FindStringSubmatch(body)
		if len(stMatch) < 2 || len(ctrlMatch) < 2 {
			continue
		}
		st := strings.TrimSpace(stMatch[1])
		if strings.Contains(st, "WANIPConnection") || strings.Contains(st, "WANPPPConnection") {
			baseU, parseErr := url.Parse(descURL)
			if parseErr != nil {
				return "", "", parseErr
			}
			relU, parseErr := url.Parse(strings.TrimSpace(ctrlMatch[1]))
			if parseErr != nil {
				return "", "", parseErr
			}
			return baseU.ResolveReference(relU).String(), st, nil
		}
	}
	return "", "", errors.New("WANIPConnection service not found in UPnP description")
}

func discoverLocalOutboundIP(targetURL string) (string, error) {
	u, err := url.Parse(targetURL)
	if err != nil {
		return "", err
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	c, err := net.DialTimeout("udp4", host, 1*time.Second)
	if err != nil {
		return "", err
	}
	defer c.Close()
	udpAddr, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", errors.New("failed to determine local UDP address")
	}
	return udpAddr.IP.String(), nil
}

func sendUPnPAddPortMapping(controlURL, serviceType string, port int, internalClient string, leaseSec int, proto string) error {
	soapBody := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:AddPortMapping xmlns:u="%s">
<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>%s</NewProtocol>
<NewInternalPort>%d</NewInternalPort>
<NewInternalClient>%s</NewInternalClient>
<NewEnabled>1</NewEnabled>
<NewPortMappingDescription>VeloceTorrent</NewPortMappingDescription>
<NewLeaseDuration>%d</NewLeaseDuration>
</u:AddPortMapping>
</s:Body>
</s:Envelope>`, serviceType, port, proto, port, internalClient, leaseSec)

	return postSOAPAction(controlURL, serviceType+"#AddPortMapping", soapBody, nil)
}

func sendUPnPDeletePortMapping(controlURL, serviceType string, port int, proto string) error {
	soapBody := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:DeletePortMapping xmlns:u="%s">
<NewRemoteHost></NewRemoteHost>
<NewExternalPort>%d</NewExternalPort>
<NewProtocol>%s</NewProtocol>
</u:DeletePortMapping>
</s:Body>
</s:Envelope>`, serviceType, port, proto)

	return postSOAPAction(controlURL, serviceType+"#DeletePortMapping", soapBody, nil)
}

func queryUPnPExternalIP(controlURL, serviceType string) (string, error) {
	soapBody := fmt.Sprintf(`<?xml version="1.0"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
<s:Body>
<u:GetExternalIPAddress xmlns:u="%s"></u:GetExternalIPAddress>
</s:Body>
</s:Envelope>`, serviceType)

	var respBytes []byte
	if err := postSOAPAction(controlURL, serviceType+"#GetExternalIPAddress", soapBody, &respBytes); err != nil {
		return "", err
	}
	m := reExtIP.FindStringSubmatch(string(respBytes))
	if len(m) >= 2 {
		return strings.TrimSpace(m[1]), nil
	}
	return "", errors.New("external IP not found in SOAP response")
}

func postSOAPAction(controlURL, soapAction, body string, outBody *[]byte) error {
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodPost, controlURL, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	req.Header.Set("SOAPAction", "\""+soapAction+"\"")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if outBody != nil {
		*outBody = raw
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("UPnP SOAP HTTP %d", resp.StatusCode)
	}
	return nil
}

// --- NAT-PMP (RFC 6886) Implementation ---

func (m *NATMapper) tryNATPMPMapping(port int, lifetimeSec uint32) (extIP string, extPort int, gw net.IP, err error) {
	candidates := candidateGateways()
	for _, gwIP := range candidates {
		ipStr, err := queryNATPMPExternalIP(gwIP)
		if err != nil {
			continue
		}
		mappedTCP, err := sendNATPMPPortMap(gwIP, 2, port, port, lifetimeSec) // opcode 2 = TCP
		if err != nil {
			continue
		}
		_, _ = sendNATPMPPortMap(gwIP, 1, port, port, lifetimeSec) // opcode 1 = UDP
		return ipStr, mappedTCP, gwIP, nil
	}
	return "", 0, nil, errors.New("no NAT-PMP gateway responded")
}

func candidateGateways() []net.IP {
	seen := make(map[string]struct{})
	var out []net.IP

	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			gw := net.IPv4(ip4[0], ip4[1], ip4[2], 1)
			if _, exists := seen[gw.String()]; !exists {
				seen[gw.String()] = struct{}{}
				out = append(out, gw)
			}
		}
	}
	return out
}

func queryNATPMPExternalIP(gw net.IP) (string, error) {
	raddr := &net.UDPAddr{IP: gw, Port: 5351}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(600 * time.Millisecond))

	// Version 0, Opcode 0 = External Address Request
	if _, err := conn.Write([]byte{0, 0}); err != nil {
		return "", err
	}
	var resp [12]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return "", err
	}
	if resp[1] != 128 || binary.BigEndian.Uint16(resp[2:4]) != 0 {
		return "", errors.New("NAT-PMP external IP request rejected")
	}
	ip := net.IPv4(resp[8], resp[9], resp[10], resp[11])
	return ip.String(), nil
}

func sendNATPMPPortMap(gw net.IP, opcode byte, internalPort, externalPort int, lifetimeSec uint32) (int, error) {
	raddr := &net.UDPAddr{IP: gw, Port: 5351}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(600 * time.Millisecond))

	var req [12]byte
	req[0] = 0 // Version 0
	req[1] = opcode
	binary.BigEndian.PutUint16(req[4:6], uint16(internalPort))
	binary.BigEndian.PutUint16(req[6:8], uint16(externalPort))
	binary.BigEndian.PutUint32(req[8:12], lifetimeSec)

	if _, err := conn.Write(req[:]); err != nil {
		return 0, err
	}

	var resp [16]byte
	if _, err := io.ReadFull(conn, resp[:]); err != nil {
		return 0, err
	}
	if resp[1] != 128+opcode || binary.BigEndian.Uint16(resp[2:4]) != 0 {
		return 0, errors.New("NAT-PMP port mapping rejected")
	}
	mappedPort := int(binary.BigEndian.Uint16(resp[10:12]))
	return mappedPort, nil
}
