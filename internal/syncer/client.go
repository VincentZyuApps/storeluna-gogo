package syncer

import (
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/VincentZyuApps/storeluna-gogo/internal/config"
)

func BuildHTTPClient(timeout time.Duration, proxyCfg config.ProxyConfig) *http.Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}

	if proxyCfg.Enabled && strings.TrimSpace(proxyCfg.URL) != "" {
		rawURL := strings.TrimSpace(proxyCfg.URL)
		if strings.HasPrefix(rawURL, "socks5h://") {
			rawURL = "socks5://" + strings.TrimPrefix(rawURL, "socks5h://")
		}
		if u, err := url.Parse(rawURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}
