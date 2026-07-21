package httpapi

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
)

func (s *Server) requireWorkerIP() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(s.cfg.WorkerAPIAllowlist) == 0 {
			c.Next()
			return
		}
		address, err := workerSourceIP(c.Request, s.cfg.WorkerAPITrustedProxies)
		if err != nil || !prefixesContain(s.cfg.WorkerAPIAllowlist, address) {
			problem(c, http.StatusForbidden, "Worker API 来源 IP 不在白名单中", err)
			c.Abort()
			return
		}
		c.Next()
	}
}

func workerSourceIP(request *http.Request, trusted []netip.Prefix) (netip.Addr, error) {
	peer, err := parseRemoteAddress(request.RemoteAddr)
	if err != nil {
		return netip.Addr{}, err
	}
	if !prefixesContain(trusted, peer) {
		return peer, nil
	}
	forwarded := strings.TrimSpace(request.Header.Get("X-Forwarded-For"))
	if forwarded == "" {
		return peer, nil
	}
	chain := strings.Split(forwarded, ",")
	current := peer
	for index := len(chain) - 1; index >= 0 && prefixesContain(trusted, current); index-- {
		candidate, parseErr := netip.ParseAddr(strings.TrimSpace(chain[index]))
		if parseErr != nil {
			return netip.Addr{}, errors.New("可信代理提供了无效的 X-Forwarded-For")
		}
		current = candidate.Unmap()
	}
	return current, nil
}

func parseRemoteAddress(value string) (netip.Addr, error) {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		host = value
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, errors.New("无法识别 Worker API 来源 IP")
	}
	return address.Unmap(), nil
}

func prefixesContain(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
